// Package idempotency de-duplicates redelivered messages on an at-least-once transport. It is a
// pipeline middleware - unlike request validation (which needs the typed request, so it wraps the
// handler), idempotency keys on a header, which is available on the type-erased InvocationContext,
// so it composes as an ordinary middleware exactly like the .NET port's IdempotencyMiddleware.
//
// It derives an idempotency key for each message, atomically claims it in a Store, and runs the
// rest of the pipeline (including the handler) only the first time that key is seen:
//   - A message with no key opts out - it is processed normally, untracked.
//   - A completed duplicate short-circuits to an `ignored` result (a success status: the work was
//     already done, so the transport acknowledges the duplicate rather than reprocessing it).
//   - An in-progress duplicate (a concurrent first attempt still running) short-circuits to a
//     `conflict` result (a failure status: the transport redelivers and retries once the first
//     attempt has settled).
//   - On the winning attempt, a successful result records the key as completed; a failed result
//     (or a pipeline error) releases the claim so a redelivery reprocesses the message - a failure
//     is never permanently suppressed by a recorded "already done".
//
// Register it early in the pipeline - before the router/handler, but typically after logging/tracing
// so duplicates are still observed. A Store outage fails open (the message is processed): a
// de-duplication store must reduce dedup, never take the service down.
package idempotency

import (
	"context"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
)

// Status is the lifecycle state of a claimed idempotency key.
type Status int

const (
	// InProgress marks a key claimed by a first attempt that has not finished yet.
	InProgress Status = iota
	// Completed marks a key whose first attempt has finished (see Record.Successful).
	Completed
)

// Record is the stored state of a claimed key.
type Record struct {
	Status Status
	// Successful is whether the first attempt succeeded; meaningful only when Status is Completed.
	Successful bool
}

// ClaimResult is the outcome of Store.Claim.
type ClaimResult struct {
	// Won is true when this caller claimed the key for the first time (and must process the message).
	Won bool
	// Existing is the record already present when Won is false.
	Existing Record
}

// Store is pluggable persistence for idempotency keys. Swap the implementation to change where
// records live (InMemoryStore for a single instance/tests; a shared store like Redis for a
// multi-instance deployment) without touching the middleware. Implementations MUST make Claim's
// check-and-insert atomic so two concurrent redeliveries cannot both win, and MUST be safe for
// concurrent use.
type Store interface {
	// Claim atomically claims key for first-time processing: it records a new InProgress entry and
	// returns Won when no live entry exists, or returns the existing record (Won false) otherwise.
	Claim(ctx context.Context, key string) (ClaimResult, error)
	// Complete promotes a claimed key to Completed, recording whether the first attempt succeeded.
	Complete(ctx context.Context, key string, successful bool) error
	// Release removes a claim so a redelivery can reprocess the message (called on failure).
	Release(ctx context.Context, key string) error
}

// KeyFunc derives the idempotency key for an invocation, or returns "" to skip de-duplication and
// let the message through untracked.
type KeyFunc func(ic *benzene.InvocationContext) string

// DefaultKeyHeader is the header HeaderKey reads unless another is named.
const DefaultKeyHeader = "idempotency-key"

// HeaderKey returns a KeyFunc that reads the idempotency key from the named header
// (case-insensitive), scoped by topic so the same key value on two different topics does not
// collide. An absent or empty header opts the message out of de-duplication.
func HeaderKey(header string) KeyFunc {
	return func(ic *benzene.InvocationContext) string {
		for name, value := range ic.Headers {
			if value != "" && strings.EqualFold(name, header) {
				return ic.Topic.String() + "\x00" + value
			}
		}
		return ""
	}
}

// Middleware returns the de-duplication middleware described in the package doc, deriving each
// message's key with key and claiming it in store.
func Middleware(store Store, key KeyFunc) benzene.Middleware {
	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		k := key(ic)
		if k == "" {
			return next(ctx) // no key - this message opts out of de-duplication
		}

		claim, err := store.Claim(ctx, k)
		if err != nil {
			// The store is unavailable: fail open (process) rather than block the message - dedup is
			// reduced, the service is not.
			return next(ctx)
		}

		if !claim.Won {
			if claim.Existing.Status == Completed {
				ic.Result = benzene.Ignored[any](nil) // already processed - acknowledge, do not rerun
			} else {
				ic.Result = benzene.Conflict[any]("idempotency key already in progress") // retry later
			}
			return nil // short-circuit: the router/handler downstream do not run
		}

		if err := next(ctx); err != nil {
			_ = store.Release(ctx, k) // a pipeline-level error must not leave the key claimed
			return err
		}

		if resultSuccessful(ic.Result) {
			_ = store.Complete(ctx, k, true)
		} else {
			_ = store.Release(ctx, k) // the handler reported failure - let a redelivery retry
		}
		return nil
	}
}

// resultSuccessful reads the result's success flag off the type-erased ResultInfo (via the optional
// ResultIsSuccessful interface every Result[T] satisfies), falling back to the status class - the
// same rule envelope dispatch uses to decide acknowledge-vs-retry.
func resultSuccessful(result benzene.ResultInfo) bool {
	if result == nil {
		return false
	}
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		return s.ResultIsSuccessful()
	}
	return !result.ResultStatus().IsFailure()
}
