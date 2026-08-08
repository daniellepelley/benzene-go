// Package resilience provides a retry middleware for the Benzene pipeline: Middleware(opts...)
// re-invokes the downstream pipeline with exponential backoff when an attempt fails. It is
// zero-dependency and matches Benzene.Resilience - which is retry-ONLY and deliberately stays that
// way: circuit breaker, timeout, bulkhead, hedging, and fallback live in the sibling
// Benzene.Resilience.Polly (a Polly-backed package). This port keeps the retry piece, the one that
// needs no third-party library, and leaves the richer toolkit for a later dependency decision
// (ROADMAP.md).
//
// # Failure model
//
// The Go pipeline expresses a handler outcome two ways: a genuine infrastructure fault propagates
// as a Go error from next(ctx), while an ordinary application failure lands on ic.Result as an
// unsuccessful status (the router never returns a Go error for that - see RouterMiddleware). So
// retry has two independent triggers, mirroring .NET's shouldRetry(exception) +
// shouldRetryContext(context):
//
//   - WithRetryOnError (default: any error except a context cancellation) decides retry from a
//     next() error.
//   - WithRetryOnResult (default: never) decides retry from ic.Result after a non-error attempt.
//     Because the router funnels almost every outcome onto ic.Result, this is the lever most
//     services set: pass RetryUnsuccessful, or RetryOnStatus(benzene.StatusServiceUnavailable,
//     benzene.StatusTimeout) to retry only the transient statuses.
//
// # Backoff
//
// sleep = jitter(min(maxDelay, initialDelay * backoffFactor^attempt)). The cap and jitter apply only
// to the actual sleep; the exponential curve driving the next attempt's delay is grown uncapped, so
// later attempts still compound off the true curve - AWS's documented "full jitter" algorithm. The
// sleep is context-aware: a cancelled ctx aborts the wait and returns its error instead of sleeping
// on, so retry respects cancellation/deadlines the same way the rest of the pipeline does.
//
// # Placement
//
// Retry re-invokes the WHOLE downstream pipeline, so register it above the outbound/port calls that
// are safe to re-run - not on an inbound step that has already written a response, since a
// re-invocation would run those steps again. Any handler it retries must be idempotent.
package resilience

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// Defaults, matching Benzene.Resilience's RetryMiddleware constructor.
const (
	defaultRetries       = 3
	defaultInitialDelay  = 200 * time.Millisecond
	defaultBackoffFactor = 2.0
)

// config holds the resolved retry knobs.
type config struct {
	retries       int
	initialDelay  time.Duration
	backoffFactor float64
	maxDelay      time.Duration // 0 = uncapped
	retryOnError  func(error) bool
	retryOnResult func(benzene.ResultInfo) bool
	jitter        func(time.Duration) time.Duration
	sleep         func(context.Context, time.Duration) error
}

// Option configures the retry middleware.
type Option func(*config)

// WithRetries sets the maximum number of RETRIES after the first attempt (so the pipeline runs at
// most retries+1 times). Default 3.
func WithRetries(n int) Option { return func(c *config) { c.retries = n } }

// WithInitialDelay sets the first backoff delay, grown by the backoff factor each attempt. Default
// 200ms.
func WithInitialDelay(d time.Duration) Option { return func(c *config) { c.initialDelay = d } }

// WithBackoffFactor sets the multiplier the delay grows by each attempt. Default 2.0.
func WithBackoffFactor(f float64) Option { return func(c *config) { c.backoffFactor = f } }

// WithMaxDelay caps the actual sleep each attempt (0, the default, is uncapped). The exponential
// curve used to grow the NEXT attempt's delay is left uncapped, matching the "full jitter" shape.
func WithMaxDelay(d time.Duration) Option { return func(c *config) { c.maxDelay = d } }

// WithRetryOnError overrides the predicate deciding whether a Go error from next() is retried.
// Default: retry any error that is not a context.Canceled / context.DeadlineExceeded.
func WithRetryOnError(f func(error) bool) Option { return func(c *config) { c.retryOnError = f } }

// WithRetryOnResult overrides the predicate deciding whether ic.Result (after a non-error attempt)
// is retried. Default: never. Pass RetryUnsuccessful or RetryOnStatus(...).
func WithRetryOnResult(f func(benzene.ResultInfo) bool) Option {
	return func(c *config) { c.retryOnResult = f }
}

// WithJitter sets the function that turns the capped delay into the actual sleep duration. Default:
// identity (no jitter). Pass FullJitter(nil) for the AWS "full jitter" spread.
func WithJitter(f func(time.Duration) time.Duration) Option {
	return func(c *config) { c.jitter = f }
}

// WithSleep overrides the (context-aware) sleep mechanism - mainly for tests, so a retry loop runs
// without real time passing. Default: a context.Context-cancellable timer.
func WithSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *config) { c.sleep = f }
}

// Middleware returns a benzene.Middleware that retries the downstream pipeline with exponential
// backoff. See the package doc for the two retry triggers and placement guidance.
func Middleware(opts ...Option) benzene.Middleware {
	cfg := config{
		retries:       defaultRetries,
		initialDelay:  defaultInitialDelay,
		backoffFactor: defaultBackoffFactor,
		retryOnError:  defaultRetryOnError,
		retryOnResult: neverRetryResult,
		jitter:        noJitter,
		sleep:         contextSleep,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		attempt := 0
		delay := cfg.initialDelay
		for {
			err := next(ctx)
			if err != nil {
				if attempt >= cfg.retries || !cfg.retryOnError(err) {
					return err
				}
			} else if attempt >= cfg.retries || !cfg.retryOnResult(ic.Result) {
				return nil
			}

			attempt++
			capped := delay
			if cfg.maxDelay > 0 && capped > cfg.maxDelay {
				capped = cfg.maxDelay
			}
			if err := cfg.sleep(ctx, cfg.jitter(capped)); err != nil {
				// Cancelled/deadline-exceeded during backoff: stop retrying and surface it.
				return err
			}
			delay = growDelay(delay, cfg.backoffFactor)
		}
	}
}

// RetryUnsuccessful is a WithRetryOnResult predicate that retries any result the pipeline treats as
// unsuccessful (via the optional ResultIsSuccessful interface every Result[T] satisfies, falling
// back to the status class - the same rule envelope dispatch and idempotency use to decide
// acknowledge-vs-retry).
func RetryUnsuccessful(result benzene.ResultInfo) bool { return !resultSuccessful(result) }

// RetryOnStatus builds a WithRetryOnResult predicate that retries only when ic.Result carries one of
// the given statuses - e.g. RetryOnStatus(benzene.StatusServiceUnavailable, benzene.StatusTimeout)
// to retry transient failures but not a validation-error.
func RetryOnStatus(statuses ...benzene.Status) func(benzene.ResultInfo) bool {
	set := make(map[benzene.Status]struct{}, len(statuses))
	for _, s := range statuses {
		set[s] = struct{}{}
	}
	return func(result benzene.ResultInfo) bool {
		if result == nil {
			return false
		}
		_, ok := set[result.ResultStatus()]
		return ok
	}
}

// FullJitter is the AWS-recommended "full jitter" backoff: it returns a random duration in
// [0, delay). Pass it to WithJitter to spread out retries from many callers that backed off at the
// same moment, instead of them all retrying in lockstep (the "thundering herd" problem plain
// exponential backoff does not address). r may be nil, in which case the (concurrency-safe) global
// source is used.
func FullJitter(r *rand.Rand) func(time.Duration) time.Duration {
	return func(delay time.Duration) time.Duration {
		if delay <= 0 {
			return 0
		}
		var f float64
		if r != nil {
			f = r.Float64()
		} else {
			f = rand.Float64()
		}
		return time.Duration(f * float64(delay))
	}
}

func defaultRetryOnError(err error) bool {
	return !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func neverRetryResult(benzene.ResultInfo) bool { return false }

func noJitter(d time.Duration) time.Duration { return d }

// growDelay multiplies the delay by the backoff factor, clamping at the time.Duration ceiling so a
// large retry count (or initialDelay/backoffFactor) can't overflow int64 nanoseconds and wrap to a
// negative sleep. The actual sleep is already bounded by the cap; this only guards the growth.
func growDelay(d time.Duration, factor float64) time.Duration {
	next := float64(d) * factor
	if next >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(next)
}

// contextSleep waits for d, or returns early with the context's error if it is cancelled first. A
// non-positive d does not wait but still honors an already-cancelled context.
func contextSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// resultSuccessful reads the result's success flag off the type-erased ResultInfo (the optional
// ResultIsSuccessful interface every Result[T] satisfies), falling back to the status class -
// matching idempotency's resultSuccessful and envelope dispatch.
func resultSuccessful(result benzene.ResultInfo) bool {
	if result == nil {
		return false
	}
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		return s.ResultIsSuccessful()
	}
	return !result.ResultStatus().IsFailure()
}
