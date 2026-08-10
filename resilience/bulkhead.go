package resilience

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// bulkheadConfig holds the resolved bulkhead knobs (the concurrency and queue capacities are
// captured directly by Bulkhead; only the rejection outcome is configurable).
type bulkheadConfig struct {
	maxQueue       int
	rejectStatus   benzene.Status
	rejectMessages []string
}

// BulkheadOption configures the bulkhead middleware.
type BulkheadOption func(*bulkheadConfig)

// WithMaxQueue allows up to n callers to WAIT for an execution slot when all maxConcurrency slots are
// busy, instead of being rejected immediately. Default 0 - a full bulkhead fails fast with no
// queuing (the pure isolation case). A waiting caller is still bounded by its own context: if ctx is
// cancelled while queued, the wait ends and that cancellation surfaces (as the rest of the pipeline
// treats a cancellation), the slot is never taken, and the queue frees. n must be >= 0.
func WithMaxQueue(n int) BulkheadOption { return func(c *bulkheadConfig) { c.maxQueue = n } }

// WithRejectStatus sets the status of the fail-fast result written to ic.Result when the bulkhead is
// full (all slots busy and the queue, if any, full). Default: benzene.StatusTooManyRequests, matching
// the ratelimiting middleware's shed-load rejection. Must be a framework failure status; a
// success-class status panics at wiring time, matching benzene.Fail's own contract.
func WithRejectStatus(status benzene.Status) BulkheadOption {
	return func(c *bulkheadConfig) { c.rejectStatus = status }
}

// WithRejectMessages sets the human-readable error messages carried on the fail-fast result written
// when the bulkhead rejects a call. Default: a single "bulkhead is full" message.
func WithRejectMessages(messages ...string) BulkheadOption {
	return func(c *bulkheadConfig) { c.rejectMessages = messages }
}

// Bulkhead returns a benzene.Middleware that caps the number of invocations running the downstream
// pipeline concurrently at maxConcurrency, isolating a dependency (a DB pool, an outbound service)
// from being overwhelmed by a spike: past the cap, extra messages are shed fast rather than piling up
// and exhausting the resource. It is the bulkhead-isolation slice of Benzene.Resilience.Polly, and
// like retry and timeout it needs no third-party library, so it lives here in the zero-dependency
// package.
//
// # Admission
//
// By default (no queue) an invocation that arrives while all maxConcurrency slots are busy is
// REJECTED immediately: the middleware short-circuits ic.Result to a fail-fast result (default
// StatusTooManyRequests, configurable via WithRejectStatus / WithRejectMessages) and returns nil -
// a short-circuit is a Benzene result, not a transport error, exactly like the ratelimiting
// middleware's too-many-requests rejection. The downstream is never called in this state.
//
// WithMaxQueue(n) lets up to n callers WAIT for a slot instead of being rejected, bounding the total
// in-flight (running + waiting) at maxConcurrency+n; only when that ceiling is also reached is a call
// rejected. A queued caller waits on its context: a cancelled ctx ends the wait and surfaces the
// cancellation (the same way the retry backoff and the rest of the pipeline treat a cancellation),
// without ever taking a slot.
//
// # Placement
//
// Register the bulkhead ABOVE the calls whose concurrency it should bound - it must sit between the
// caller and the dependency it protects. Compose it with the other resilience pieces by ordering:
// a bulkhead above retry bounds the concurrency of the whole retry loop (retries do not each consume
// a fresh slot beyond the one the loop already holds), while a bulkhead below retry would let each
// retry re-contend for a slot.
//
// maxConcurrency must be >= 1 and (via WithMaxQueue) the queue must be >= 0; an invalid value panics
// at wiring time, never per-request, so the transport never sees a panic.
func Bulkhead(maxConcurrency int, opts ...BulkheadOption) benzene.Middleware {
	if maxConcurrency < 1 {
		panic("resilience: Bulkhead requires maxConcurrency >= 1")
	}
	cfg := bulkheadConfig{
		maxQueue:       0,
		rejectStatus:   benzene.StatusTooManyRequests,
		rejectMessages: []string{"bulkhead is full"},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxQueue < 0 {
		panic("resilience: Bulkhead requires WithMaxQueue >= 0")
	}
	// Build the short-circuit result once, at wiring time (immutable, reused for every rejection);
	// this is also where a misconfigured success-class WithRejectStatus panics - at wiring, never
	// per-request (benzene.Fail's contract).
	rejectResult := benzene.Fail[any](cfg.rejectStatus, cfg.rejectMessages...)

	// exec is the pool of execution permits (cap = maxConcurrency). admit is the pool of admission
	// permits (cap = maxConcurrency + maxQueue): a caller must first take an admission permit
	// (non-blocking - failing it means the queue is full, so reject), then take an execution permit
	// (blocking only when maxQueue > 0, since with no queue the two capacities are equal and an
	// admission permit guarantees an immediately-available execution permit). Both are released on
	// the way out. Modeled on Polly's two-semaphore bulkhead.
	exec := make(chan struct{}, maxConcurrency)
	admit := make(chan struct{}, maxConcurrency+cfg.maxQueue)

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		select {
		case admit <- struct{}{}:
			// Admitted to run or queue.
		default:
			// Admission pool full: at the running+waiting ceiling, so reject fast.
			ic.Result = rejectResult
			return nil
		}
		defer func() { <-admit }()

		select {
		case exec <- struct{}{}:
			// Got an execution slot.
		case <-ctx.Done():
			// Cancelled/deadline-exceeded while queued: give up the wait without taking a slot and
			// surface the cancellation, the same way the retry backoff does. Unreachable when
			// maxQueue == 0 (an admission permit then guarantees a free execution permit), but kept
			// so a queued caller always honors its context.
			return ctx.Err()
		}
		defer func() { <-exec }()

		return next(ctx)
	}
}
