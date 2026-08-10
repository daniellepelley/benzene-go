package resilience

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// FallbackFunc provides a substitute outcome when the primary attempt is deemed a failure. It
// receives the invocation context - so it can read the request and set a substitute ic.Result (a
// cached value, a default response, a degraded-mode payload) - and the cause: the Go error next()
// returned, or nil when the trigger was an unsuccessful ic.Result rather than an error. It should set
// ic.Result to the substitute and return nil; returning an error propagates that error instead,
// modeling a fallback that itself failed.
type FallbackFunc func(ctx context.Context, ic *benzene.InvocationContext, cause error) error

// fallbackConfig holds the resolved fallback triggers.
type fallbackConfig struct {
	onError  func(error) bool
	onResult func(benzene.ResultInfo) bool
}

// FallbackOption configures the fallback middleware.
type FallbackOption func(*fallbackConfig)

// WithFallbackOnError overrides the predicate deciding whether a Go error from next() triggers the
// fallback. Default: any error that is not a context.Canceled / context.DeadlineExceeded (a caller
// giving up or a deadline is not a downstream failure to substitute for) - the same rule retry uses.
func WithFallbackOnError(f func(error) bool) FallbackOption {
	return func(c *fallbackConfig) { c.onError = f }
}

// WithFallbackOnResult overrides the predicate deciding whether ic.Result (after a non-error attempt)
// triggers the fallback. Default: FallbackUnsuccessful - any result the pipeline treats as
// unsuccessful. Pass FallbackOnStatus(...) to substitute only for specific statuses (e.g. transient
// ones), letting a validation-error flow through unchanged.
func WithFallbackOnResult(f func(benzene.ResultInfo) bool) FallbackOption {
	return func(c *fallbackConfig) { c.onResult = f }
}

// Fallback returns a benzene.Middleware that runs the downstream pipeline and, when the attempt is
// deemed a failure, calls fallback to provide a substitute outcome - the graceful-degradation slice
// of Benzene.Resilience.Polly. Like retry and timeout it needs no third-party library, so it lives
// here in the zero-dependency package.
//
// # Triggers
//
// The Go pipeline expresses a handler outcome two ways - a Go error from next(ctx), or an
// unsuccessful status on ic.Result (the router never returns a Go error for an application failure) -
// so fallback has two triggers mirroring retry's:
//
//   - WithFallbackOnError (default: any error except a context cancellation) decides fallback from a
//     next() error. A non-triggering error propagates unchanged.
//   - WithFallbackOnResult (default: FallbackUnsuccessful) decides fallback from ic.Result after a
//     non-error attempt. A non-triggering result stays on ic.Result untouched.
//
// When a trigger fires, fallback(ctx, ic, cause) runs - cause is the next() error, or nil when the
// trigger was an unsuccessful result - and is responsible for setting the substitute ic.Result. A
// fallback that returns nil yields that substitute as the invocation's outcome; a fallback that
// itself returns an error propagates that error.
//
// # Placement
//
// Register the fallback ABOVE the calls it substitutes for. Compose it with the other resilience
// pieces by ordering: a fallback above retry fires only after the retries are exhausted (the last
// failure is what it substitutes for), and a fallback above a circuit breaker substitutes for the
// breaker's open-state fail-fast result too - so an open breaker degrades to the fallback response
// instead of surfacing service-unavailable, which is often exactly what you want.
//
// fallback must be non-nil; a nil fallback panics at wiring time, never per-request.
func Fallback(fallback FallbackFunc, opts ...FallbackOption) benzene.Middleware {
	if fallback == nil {
		panic("resilience: Fallback requires a non-nil fallback function")
	}
	cfg := fallbackConfig{
		onError:  defaultRetryOnError,
		onResult: FallbackUnsuccessful,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		err := next(ctx)
		if err != nil {
			if cfg.onError(err) {
				return fallback(ctx, ic, err)
			}
			return err
		}
		if cfg.onResult(ic.Result) {
			return fallback(ctx, ic, nil)
		}
		return nil
	}
}

// FallbackUnsuccessful is the default WithFallbackOnResult predicate: it triggers the fallback for any
// result the pipeline treats as unsuccessful (via the optional ResultIsSuccessful interface every
// Result[T] satisfies, falling back to the status class) - the same success-vs-failure rule
// RetryUnsuccessful and envelope dispatch use.
func FallbackUnsuccessful(result benzene.ResultInfo) bool { return !resultSuccessful(result) }

// FallbackOnStatus builds a WithFallbackOnResult predicate that triggers the fallback only for the
// given statuses - e.g. FallbackOnStatus(benzene.StatusServiceUnavailable, benzene.StatusTimeout) to
// degrade gracefully on transient downstream failures while letting a validation-error surface
// unchanged.
func FallbackOnStatus(statuses ...benzene.Status) func(benzene.ResultInfo) bool {
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
