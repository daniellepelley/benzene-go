package resilience

import (
	"context"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// Timeout returns a benzene.Middleware that bounds the downstream pipeline to d: it derives a
// context.WithTimeout(ctx, d) and passes it down, so a handler (and the I/O it drives - a DB call,
// an outbound client) that honors its context returns when the deadline elapses instead of running
// unbounded. When that deadline is what ended the attempt, the middleware presents the outcome as a
// StatusTimeout result, so callers see a uniform "timed out" status regardless of how the downstream
// surfaced the cancellation.
//
// It is COOPERATIVE, which is the only safe timeout in Go: a goroutine cannot be forcibly stopped,
// so a handler that ignores its context is not interrupted - the deadline still fires, but the wait
// is only bounded once that handler returns. Handlers that thread ctx through their calls (the
// idiomatic case) are bounded as expected. This is the same context-honoring contract the retry
// backoff and the rest of the pipeline follow; the middleware adds no goroutine and never races on
// ic.Result (it calls next synchronously and only inspects the deadline afterward).
//
// A cancellation of the PARENT ctx (the caller giving up, or a shutdown) is not relabeled as a
// timeout - only d elapsing is. And a downstream that still managed to produce a successful result
// right at the boundary is preserved rather than clobbered. Place Timeout above the work it should
// bound; combine it with Middleware (retry) by ordering them deliberately - retry-above-timeout
// retries each timed-out attempt, timeout-above-retry bounds the whole retry loop.
//
// Timeout is the one piece of the richer resilience toolkit (circuit breaker, bulkhead, hedging,
// fallback) that needs no third-party library, so it lives here in the zero-dependency package; the
// rest stay deferred to a later dependency decision (see the package doc and ROADMAP.md).
func Timeout(d time.Duration) benzene.Middleware {
	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		timeoutCtx, cancel := context.WithTimeout(ctx, d)
		defer cancel()

		err := next(timeoutCtx)

		// Only our own deadline counts: a parent-ctx cancellation is the caller/shutdown giving up,
		// not the handler exceeding d, so it is left to surface as-is.
		if ctx.Err() == nil && timeoutCtx.Err() == context.DeadlineExceeded {
			// Preserve a genuine success produced right at the boundary; otherwise present the
			// outcome as a timeout (whether the downstream errored, produced nothing, or produced an
			// unsuccessful result off the cancelled context).
			if err == nil && resultSuccessful(ic.Result) {
				return nil
			}
			ic.Result = benzene.Timeout[any]("operation exceeded the " + d.String() + " timeout")
			return nil
		}
		return err
	}
}
