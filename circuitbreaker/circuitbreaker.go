// Package circuitbreaker wraps a circuit breaker around the downstream Benzene pipeline: while the
// breaker is CLOSED, invocations pass through and failures are counted; once failures cross the
// trip threshold the breaker OPENS and further invocations short-circuit immediately (fail fast)
// without touching the downstream, until the breaker's timeout lets it try again (HALF-OPEN).
//
// It is the circuit-breaker slice of Benzene.Resilience.Polly, the sibling of the zero-dependency
// resilience package (retry + timeout). Retry re-invokes a call that just failed; a circuit breaker
// does the opposite - it stops calling a dependency that keeps failing, so a struggling downstream
// is given room to recover instead of being hammered, and callers fail fast instead of piling up on
// a doomed request. The breaker is the piece that genuinely wants a battle-tested library, so this
// package takes a dependency on github.com/sony/gobreaker/v2 and - like awssqs and the other
// dependency-carrying bindings - lives in its own Go module so that dependency does not spread to
// the zero-dependency root.
//
// # Failure model
//
// The Go pipeline expresses a handler outcome two ways: a genuine infrastructure fault propagates
// as a Go error from next(ctx), while an ordinary application failure lands on ic.Result as an
// unsuccessful status (the router never returns a Go error for that - see RouterMiddleware). This
// middleware trips the breaker on BOTH, mirroring resilience's two retry triggers:
//
//   - a genuine error from next() counts as a failure to the breaker AND propagates up unchanged;
//   - a successful next() whose ic.Result is UNSUCCESSFUL (per the configurable WithTripOnResult
//     predicate, default TripUnsuccessful) counts as a failure to the breaker, but the unsuccessful
//     result stays on ic.Result and the middleware returns nil - that result IS the Benzene outcome,
//     not a transport error;
//   - a successful result does not count as a failure.
//
// # Short-circuit
//
// When the breaker is OPEN (or HALF-OPEN and already at its probe limit), gobreaker rejects the call
// WITHOUT invoking the downstream. The middleware then short-circuits: it sets ic.Result to a
// fail-fast result (default StatusServiceUnavailable, configurable via WithOpenStatus /
// WithOpenMessages) and returns nil - a short-circuit is a Benzene result, not a transport error,
// exactly like the ratelimiting middleware's too-many-requests rejection. The downstream is never
// called in this state, which is the whole point of failing fast.
//
// # Placement
//
// Register the breaker ABOVE the outbound/port calls it should protect - it must sit between the
// caller and the dependency whose failures it counts. Compose it with resilience by ordering: put
// retry BELOW the breaker (retry -> the call) so a burst of retries against a dead dependency still
// counts toward tripping and, once open, the breaker fails fast before retry even starts; a timeout
// belongs below the breaker too, so an elapsed deadline is one of the failures that can trip it.
package circuitbreaker

import (
	"context"
	"errors"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/sony/gobreaker/v2"
)

// errUnsuccessfulResult is the sentinel the middleware hands back to gobreaker.Execute so an
// unsuccessful ic.Result (a non-error application failure) is counted as a failure against the
// breaker. It never leaves this package: the middleware recognizes it, leaves the real result on
// ic.Result, and returns nil to the transport.
var errUnsuccessfulResult = errors.New("circuitbreaker: unsuccessful result")

// config holds the resolved breaker knobs (everything not owned by the gobreaker.CircuitBreaker
// itself - the trip threshold, timeout, and half-open probe count live on the CircuitBreaker's
// Settings, which the caller constructs).
type config struct {
	tripOnResult func(benzene.ResultInfo) bool
	openStatus   benzene.Status
	openMessages []string
}

// Option configures the circuit-breaker middleware.
type Option func(*config)

// WithTripOnResult overrides the predicate deciding whether a (non-error) ic.Result counts as a
// failure against the breaker. Default: TripUnsuccessful - any result the pipeline treats as
// unsuccessful. Pass TripOnStatus(...) to trip only on specific transient statuses.
func WithTripOnResult(f func(benzene.ResultInfo) bool) Option {
	return func(c *config) { c.tripOnResult = f }
}

// WithOpenStatus sets the status of the fail-fast result written to ic.Result when the breaker is
// open and short-circuits a call. Default: benzene.StatusServiceUnavailable. Must be a framework
// failure status (the short-circuit is a failure); a success-class status panics at wiring time,
// matching benzene.Fail's own contract.
func WithOpenStatus(status benzene.Status) Option {
	return func(c *config) { c.openStatus = status }
}

// WithOpenMessages sets the human-readable error messages carried on the fail-fast result written
// when the breaker short-circuits a call. Default: a single "circuit breaker is open" message.
func WithOpenMessages(messages ...string) Option {
	return func(c *config) { c.openMessages = messages }
}

// Middleware returns a benzene.Middleware that runs the downstream pipeline inside cb. A genuine
// next() error and an unsuccessful ic.Result both count as failures against the breaker (see the
// package doc); once the breaker is open, calls short-circuit to a fail-fast result without touching
// the downstream. T is the breaker's result type parameter and is irrelevant here - the handler
// writes ic.Result, not a returned value - so any type works (the caller typically uses struct{}).
func Middleware[T any](cb *gobreaker.CircuitBreaker[T], opts ...Option) benzene.Middleware {
	cfg := config{
		tripOnResult: TripUnsuccessful,
		openStatus:   benzene.StatusServiceUnavailable,
		openMessages: []string{"circuit breaker is open"},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	// Build the short-circuit result once, at wiring time: it is immutable and reused for every
	// rejected call. This is also where a misconfigured success-class WithOpenStatus panics - at
	// wiring, never per-request, so the transport never sees a panic (benzene.Fail's contract).
	openResult := benzene.Fail[any](cfg.openStatus, cfg.openMessages...)

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		called := false
		_, err := cb.Execute(func() (T, error) {
			called = true
			var zero T
			if nerr := next(ctx); nerr != nil {
				// A genuine downstream error: count it as a failure AND propagate it unchanged.
				return zero, nerr
			}
			if cfg.tripOnResult(ic.Result) {
				// An unsuccessful result: count it as a failure to the breaker, but keep it on
				// ic.Result - the sentinel is stripped below and never reaches the transport.
				return zero, errUnsuccessfulResult
			}
			return zero, nil
		})

		if !called {
			// gobreaker rejected the call before running the closure (open, or half-open at its
			// probe limit - the ErrOpenState / ErrTooManyRequests sentinels). Fail fast: the
			// downstream was never touched, and the fail-fast result is the Benzene outcome.
			ic.Result = openResult
			return nil
		}
		if errors.Is(err, errUnsuccessfulResult) {
			// The downstream ran and produced an unsuccessful result (already on ic.Result); the
			// error existed only to trip the breaker and does not propagate to the transport.
			return nil
		}
		// nil on success, or the genuine downstream error to propagate.
		return err
	}
}

// TripUnsuccessful is the default WithTripOnResult predicate: it counts any result the pipeline
// treats as unsuccessful (via the optional ResultIsSuccessful interface every Result[T] satisfies,
// falling back to the status class) as a failure against the breaker - the same rule
// resilience.RetryUnsuccessful, envelope dispatch, and idempotency use to decide success-vs-failure.
func TripUnsuccessful(result benzene.ResultInfo) bool { return !resultSuccessful(result) }

// TripOnStatus builds a WithTripOnResult predicate that counts only the given statuses as failures -
// e.g. TripOnStatus(benzene.StatusServiceUnavailable, benzene.StatusTimeout) to trip on transient
// downstream failures while letting a validation-error flow through without moving the breaker.
func TripOnStatus(statuses ...benzene.Status) func(benzene.ResultInfo) bool {
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

// resultSuccessful reads the result's success flag off the type-erased ResultInfo (the optional
// ResultIsSuccessful interface every Result[T] satisfies), falling back to the status class -
// matching resilience.resultSuccessful, idempotency, and envelope dispatch.
func resultSuccessful(result benzene.ResultInfo) bool {
	if result == nil {
		return false
	}
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		return s.ResultIsSuccessful()
	}
	return !result.ResultStatus().IsFailure()
}

// Compile-time assertion that Middleware yields a benzene.Middleware. A nil breaker is fine here:
// Middleware only touches cb inside the returned closure, which this assertion never invokes.
var _ benzene.Middleware = Middleware[struct{}](nil)
