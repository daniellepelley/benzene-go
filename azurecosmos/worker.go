package azurecosmos

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// ChangeFeedPage is a transport-neutral view of one page of the change feed: the batch of changed
// documents plus the continuation token to read the NEXT page. It is deliberately this package's own
// small struct - not the SDK's azcosmos.ChangeFeedResponse - so the Worker's poll-and-dispatch logic
// is exercisable against a fake with no SDK dependency in the test.
type ChangeFeedPage struct {
	// Documents are the changed documents in this page, each a raw JSON document. They are dispatched
	// as one fan-in batch (a JSON array) to the Worker's Topic. An empty slice means the reader is
	// caught up (no changes since the last read).
	Documents []json.RawMessage
	// Continuation is the opaque token to pass to the next ReadNext to continue after this page. It is
	// what a Checkpoint hook persists for durable resume; treat it as opaque.
	Continuation string
}

// ChangeFeedReader is the narrow inbound interface the Worker depends on: read the next page of the
// change feed starting after continuation (an empty continuation means start from the reader's
// configured beginning), returning up to maxItems documents. A concrete reader (NewChangeFeedReader,
// over a real *azcosmos.ContainerClient) owns how it connects and which range it reads. Depending on
// this interface, not the concrete SDK client, keeps the Worker testable with a fake - no live Cosmos.
type ChangeFeedReader interface {
	ReadNext(ctx context.Context, continuation string, maxItems int) (ChangeFeedPage, error)
}

// Worker runs a Benzene pipeline over a Cosmos DB change feed. It polls the change feed through a
// ChangeFeedReader and dispatches each non-empty batch of changed documents as ONE fan-in pipeline
// invocation (its own DI scope, via envelope.DispatchTopicResult) to Topic - not one invocation per
// document. On a successful dispatch the optional Checkpoint hook is called with the new continuation
// token so the caller can persist durable progress; on an unsuccessful dispatch the token is not
// advanced, so the batch is re-read (redelivered). It is the self-hosted counterpart of the
// zero-dependency azurefunctions.CosmosHandler (the package doc explains why the lease container /
// checkpointing is caller-provided rather than owned here).
type Worker struct {
	// Reader is the change-feed reader each poll reads from (typically wraps a real
	// *azcosmos.ContainerClient via NewChangeFeedReader). Required.
	Reader ChangeFeedReader
	// Builder is the application whose pipeline each batch dispatches through. Required.
	Builder *benzene.ApplicationBuilder
	// Topic is the code-named topic every batch fans in to (fan-in, not topic-routed - like
	// azurefunctions.CosmosHandler). Its handler receives the batch as its request, idiomatically a
	// slice. Required.
	Topic benzene.Topic

	// StartContinuation is the continuation token to resume the change feed from - typically loaded
	// from the caller's durable store (the same value a previous Checkpoint persisted). Leave it empty
	// to start from the reader's configured beginning.
	StartContinuation string

	// MaxBatchSize is the maximum number of documents one poll may return (the change feed's
	// MaxItemCount hint; the service may return fewer). Default 100 when <= 0.
	MaxBatchSize int

	// PollInterval is how long Run waits after a caught-up (empty) poll before polling again, so an
	// idle change feed is not hammered in a tight loop. Unlike the Event Hubs receiver, the change feed
	// read returns immediately when there are no changes, so this poll pacing is required. Default 5s
	// when 0.
	PollInterval time.Duration

	// ErrorBackoff is how long Run waits after a reader error, an unsuccessful dispatch, or a
	// checkpoint error before polling again, so a transient outage or a poison batch does not become a
	// hot loop. Default 1s when 0.
	ErrorBackoff time.Duration

	// OnFailure, when non-nil, is called for a batch whose dispatch result is not a success status.
	// The continuation token is NOT advanced (the batch redelivers on the next poll and on restart), so
	// - like the ordered-stream sibling azureeventhub.Consumer - this is stop-at-batch-failure: the
	// durable position never moves past unhandled work. Use it to log or emit a metric; the change feed
	// has no broker-side nack, so a failure is "not checkpointed", not an explicit reject.
	OnFailure func(ctx context.Context, page ChangeFeedPage, resp wire.Response)

	// Checkpoint, when non-nil, is called with the new continuation token after a batch dispatches
	// successfully so the caller can persist durable progress (e.g. to a lease container). A returned
	// error is treated like a transient error: Run backs off and does NOT advance the token, so the
	// batch re-delivers (at-least-once) rather than being silently skipped.
	Checkpoint func(ctx context.Context, continuation string) error

	// sleep is the backoff primitive, injectable for tests. nil means sleepContext.
	sleep func(ctx context.Context, d time.Duration)
}

// errMissingDeps guards the required fields with a clear message rather than a nil dereference deep
// in the poll loop.
var errMissingDeps = errors.New("azurecosmos: Worker requires Reader, Builder, and Topic")

// Validate reports whether the Worker is runnable - call it at startup for a clear error instead of a
// panic from Run's first poll.
func (w *Worker) Validate() error {
	if w.Reader == nil || w.Builder == nil || w.Topic.ID == "" {
		return errMissingDeps
	}
	return nil
}

func (w *Worker) maxBatchSize() int {
	if w.MaxBatchSize <= 0 {
		return 100
	}
	return w.MaxBatchSize
}

func (w *Worker) pollInterval() time.Duration {
	if w.PollInterval == 0 {
		return 5 * time.Second
	}
	return w.PollInterval
}

func (w *Worker) errorBackoff() time.Duration {
	if w.ErrorBackoff == 0 {
		return time.Second
	}
	return w.ErrorBackoff
}

func (w *Worker) sleepFor(ctx context.Context, d time.Duration) {
	if w.sleep != nil {
		w.sleep(ctx, d)
		return
	}
	sleepContext(ctx, d)
}

// Run polls and dispatches until ctx is cancelled, then returns ctx.Err(). No poll outcome is fatal:
// a reader error, an unsuccessful dispatch, or a checkpoint error backs off (ErrorBackoff) and keeps
// polling; a caught-up (empty) poll waits PollInterval; a successful dispatch polls again immediately
// to drain the rest of the feed. Run one Worker per goroutine; the change feed's at-least-once
// delivery means handlers must be idempotent. Call Validate first.
func (w *Worker) Run(ctx context.Context) error {
	continuation := w.StartContinuation
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, backoff := w.pollAndDispatch(ctx, continuation)
		continuation = next
		if backoff > 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.sleepFor(ctx, backoff)
		}
	}
}

// pollAndDispatch runs one read/dispatch/checkpoint cycle from the given continuation token and
// returns the continuation to use next plus how long to back off before the next poll (0 = poll
// again immediately). It is separated from Run so tests can drive a single deterministic cycle while
// Run wraps it in the lifecycle loop and owns the token across iterations.
//
// The token advances only on progress the position may safely move past: a successful dispatch (then
// Checkpoint persists it) and a caught-up empty page. A reader error, an unsuccessful dispatch, and a
// checkpoint error all return the UNCHANGED token, so the same batch is re-read - the durable
// position never advances past unhandled work.
func (w *Worker) pollAndDispatch(ctx context.Context, continuation string) (next string, backoff time.Duration) {
	page, err := w.Reader.ReadNext(ctx, continuation, w.maxBatchSize())
	if err != nil {
		return continuation, w.errorBackoff()
	}
	if len(page.Documents) == 0 {
		// Caught up: nothing to dispatch. Adopt the reader's advanced token and pace the next poll so
		// an idle feed is not polled in a tight loop. An empty page did no work, so it is not
		// checkpointed - the durable position is only advanced by a real dispatch.
		return page.Continuation, w.pollInterval()
	}

	headers, body := buildBatch(page.Documents)
	resp, successful := envelope.DispatchTopicResult(ctx, w.Builder.Pipeline, w.Builder.Container, w.Topic, headers, body)
	if !successful {
		// Stop-at-batch-failure: report and back off WITHOUT advancing the token, so the same batch is
		// re-read on the next poll (and, absent a checkpoint, on restart) rather than being skipped.
		if w.OnFailure != nil {
			w.OnFailure(ctx, page, resp)
		}
		return continuation, w.errorBackoff()
	}

	if w.Checkpoint != nil {
		// Checkpoint on a cancellation-detached context so a graceful shutdown between a successful
		// dispatch and its checkpoint still persists the handled progress, rather than cancelling the
		// checkpoint and re-reading an already-processed batch - the same settlement-outlives-
		// cancellation choice azureeventhub.Consumer and awssqs.Consumer make. On a checkpoint error the
		// token is not advanced, so the batch re-delivers (at-least-once); handlers must be idempotent.
		if err := w.Checkpoint(context.WithoutCancel(ctx), page.Continuation); err != nil {
			return continuation, w.errorBackoff()
		}
	}
	// Advance and poll again immediately to drain the rest of the feed; the next caught-up poll paces
	// itself with PollInterval.
	return page.Continuation, 0
}

// buildBatch resolves the fan-in batch for a change-feed page exactly as azurefunctions.CosmosHandler
// does: the changed documents marshalled as one JSON array become the body, and cosmos-document-count
// is the only synthesized header (useful to logging/middleware without re-parsing the batch). An empty
// batch normalizes to "[]" (not JSON null), matching CosmosHandler, so a []TDoc handler always sees a
// valid array. The documents are already valid JSON, so marshalling a []json.RawMessage cannot fail in
// practice; degrade to an empty array rather than panic if it somehow ever does.
func buildBatch(documents []json.RawMessage) (map[string]string, string) {
	headers := map[string]string{"cosmos-document-count": strconv.Itoa(len(documents))}
	if len(documents) == 0 {
		return headers, "[]"
	}
	body, err := json.Marshal(documents)
	if err != nil {
		body = []byte("[]")
	}
	return headers, string(body)
}

// sleepContext sleeps for d or until ctx is cancelled, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
