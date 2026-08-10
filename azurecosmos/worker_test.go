package azurecosmos

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// changedDoc is one changed document a fan-in handler receives; the handler takes the whole batch as
// a slice, the streaming-shaped fan-in contract.
type changedDoc struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type batchResponse struct {
	Handled int `json:"handled"`
}

// batchHandler is the fan-in handler: one invocation per batch, the batch as a slice. It fails the
// whole batch if any document has an empty name, so a test can drive an unsuccessful dispatch.
func batchHandler(_ context.Context, docs []changedDoc) benzene.Result[batchResponse] {
	for _, d := range docs {
		if d.Name == "" {
			return benzene.BadRequest[batchResponse]("a changed document is missing name")
		}
	}
	return benzene.Ok(batchResponse{Handled: len(docs)})
}

const changedTopic = "orders:changed"

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic(changedTopic), benzene.Handler[[]changedDoc, batchResponse](batchHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

// fakeReader replays a scripted sequence of ReadNext results; once drained it cancels the run's
// context and reports cancellation, the shape a real reader takes when its context is cancelled with
// nothing pending. Everything runs on Run's own goroutine, so no locking is needed.
type fakeReader struct {
	results     []readResult
	idx         int
	calls       int
	lastMax     int
	lastCont    string
	seenConts   []string
	cancel      context.CancelFunc
	cancelOnErr bool
}

type readResult struct {
	page ChangeFeedPage
	err  error
}

func (f *fakeReader) ReadNext(_ context.Context, continuation string, maxItems int) (ChangeFeedPage, error) {
	f.calls++
	f.lastMax = maxItems
	f.lastCont = continuation
	f.seenConts = append(f.seenConts, continuation)
	if f.idx >= len(f.results) {
		if f.cancel != nil {
			f.cancel()
		}
		return ChangeFeedPage{}, context.Canceled
	}
	r := f.results[f.idx]
	f.idx++
	if r.err != nil && f.cancelOnErr && f.cancel != nil {
		f.cancel()
	}
	return r.page, r.err
}

func docsPage(cont string, names ...string) ChangeFeedPage {
	docs := make([]json.RawMessage, len(names))
	for i, n := range names {
		docs[i] = json.RawMessage(`{"id":"` + n + `","name":"` + n + `"}`)
	}
	return ChangeFeedPage{Documents: docs, Continuation: cont}
}

func newRunnableWorker(t *testing.T, r ChangeFeedReader) *Worker {
	t.Helper()
	return &Worker{Reader: r, Builder: newTestBuilder(t), Topic: benzene.NewTopic(changedTopic)}
}

func TestWorker_DispatchesBatchAndCheckpoints(t *testing.T) {
	reader := &fakeReader{results: []readResult{{page: docsPage("tok-1", "One", "Two")}}}
	var checkpointed []string
	worker := newRunnableWorker(t, reader)
	worker.Checkpoint = func(_ context.Context, cont string) error {
		checkpointed = append(checkpointed, cont)
		return nil
	}

	next, backoff := worker.pollAndDispatch(context.Background(), "tok-0")
	if next != "tok-1" {
		t.Errorf("next continuation = %q, want the page's token %q", next, "tok-1")
	}
	if backoff != 0 {
		t.Errorf("backoff = %v, want 0 (drain immediately after a successful batch)", backoff)
	}
	if len(checkpointed) != 1 || checkpointed[0] != "tok-1" {
		t.Errorf("checkpointed = %v, want [tok-1] (the new continuation token)", checkpointed)
	}
	if reader.lastCont != "tok-0" {
		t.Errorf("ReadNext continuation = %q, want the starting token %q", reader.lastCont, "tok-0")
	}
}

func TestWorker_BatchIsFannedInAsOneInvocation(t *testing.T) {
	// Fan-in: the whole batch is ONE invocation whose request is the slice of all changed documents. A
	// handler that counts invocations and documents proves it got all documents in a single call rather
	// than one call per document.
	var invocations int
	var gotCount int
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic(changedTopic),
		benzene.Handler[[]changedDoc, batchResponse](func(_ context.Context, docs []changedDoc) benzene.Result[batchResponse] {
			invocations++
			gotCount = len(docs)
			return benzene.Ok(batchResponse{Handled: len(docs)})
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
	reader := &fakeReader{results: []readResult{{page: docsPage("tok-1", "A", "B", "C")}}}
	worker := &Worker{Reader: reader, Builder: builder, Topic: benzene.NewTopic(changedTopic)}

	next, backoff := worker.pollAndDispatch(context.Background(), "")
	if next != "tok-1" || backoff != 0 {
		t.Fatalf("pollAndDispatch = (%q, %v), want (\"tok-1\", 0)", next, backoff)
	}
	if invocations != 1 {
		t.Errorf("handler invoked %d times, want 1 (one fan-in invocation for the whole batch)", invocations)
	}
	if gotCount != 3 {
		t.Errorf("handler saw %d documents, want 3 (the whole batch in one call)", gotCount)
	}
}

func TestWorker_FailedBatchInvokesOnFailureAndDoesNotAdvance(t *testing.T) {
	// A document with an empty name fails the batch handler. The continuation must NOT advance and no
	// checkpoint may run, so the batch redelivers.
	badPage := ChangeFeedPage{Documents: []json.RawMessage{json.RawMessage(`{"id":"x","name":""}`)}, Continuation: "tok-1"}
	reader := &fakeReader{results: []readResult{{page: badPage}}}
	var failures []wire.Response
	checkpointCalls := 0
	worker := newRunnableWorker(t, reader)
	worker.OnFailure = func(_ context.Context, _ ChangeFeedPage, resp wire.Response) { failures = append(failures, resp) }
	worker.Checkpoint = func(_ context.Context, _ string) error { checkpointCalls++; return nil }

	next, backoff := worker.pollAndDispatch(context.Background(), "tok-0")
	if next != "tok-0" {
		t.Errorf("next continuation = %q, want the UNCHANGED token %q (batch must redeliver)", next, "tok-0")
	}
	if backoff != time.Second {
		t.Errorf("backoff = %v, want the default ErrorBackoff 1s on a failed batch", backoff)
	}
	if len(failures) != 1 {
		t.Fatalf("OnFailure called %d times, want 1", len(failures))
	}
	if failures[0].StatusCode != string(benzene.StatusBadRequest) {
		t.Errorf("failure StatusCode = %q, want %q", failures[0].StatusCode, benzene.StatusBadRequest)
	}
	if checkpointCalls != 0 {
		t.Errorf("checkpoint called %d times on a failed batch, want 0", checkpointCalls)
	}
}

func TestWorker_FailedBatchWithoutOnFailureStillDoesNotAdvance(t *testing.T) {
	badPage := ChangeFeedPage{Documents: []json.RawMessage{json.RawMessage(`{"id":"x","name":""}`)}, Continuation: "tok-1"}
	reader := &fakeReader{results: []readResult{{page: badPage}}}
	worker := newRunnableWorker(t, reader) // no OnFailure hook set

	next, backoff := worker.pollAndDispatch(context.Background(), "tok-0")
	if next != "tok-0" {
		t.Errorf("next = %q, want the unchanged token even without an OnFailure hook", next)
	}
	if backoff != time.Second {
		t.Errorf("backoff = %v, want 1s", backoff)
	}
}

func TestWorker_EmptyPageAdvancesTokenAndPaces(t *testing.T) {
	// A caught-up empty page: no dispatch, no checkpoint, but the token advances to the reader's new
	// position and Run paces the next poll with PollInterval.
	reader := &fakeReader{results: []readResult{{page: ChangeFeedPage{Continuation: "tok-empty"}}}}
	checkpointCalls := 0
	worker := newRunnableWorker(t, reader)
	worker.Checkpoint = func(_ context.Context, _ string) error { checkpointCalls++; return nil }

	next, backoff := worker.pollAndDispatch(context.Background(), "tok-0")
	if next != "tok-empty" {
		t.Errorf("next = %q, want the advanced empty-page token %q", next, "tok-empty")
	}
	if backoff != 5*time.Second {
		t.Errorf("backoff = %v, want the default PollInterval 5s on a caught-up feed", backoff)
	}
	if checkpointCalls != 0 {
		t.Errorf("checkpoint called %d times on an empty page, want 0", checkpointCalls)
	}
}

func TestWorker_ReaderErrorBacksOffWithoutAdvancing(t *testing.T) {
	reader := &fakeReader{results: []readResult{{err: errors.New("cosmos unreachable")}}}
	worker := newRunnableWorker(t, reader)

	next, backoff := worker.pollAndDispatch(context.Background(), "tok-0")
	if next != "tok-0" {
		t.Errorf("next = %q, want the unchanged token %q on a reader error", next, "tok-0")
	}
	if backoff != time.Second {
		t.Errorf("backoff = %v, want the default ErrorBackoff 1s", backoff)
	}
}

func TestWorker_CheckpointErrorBacksOffAndDoesNotAdvance(t *testing.T) {
	// The batch dispatched successfully but the durable checkpoint failed: the token must NOT advance,
	// so the batch redelivers (at-least-once) rather than being silently skipped.
	reader := &fakeReader{results: []readResult{{page: docsPage("tok-1", "One")}}}
	worker := newRunnableWorker(t, reader)
	worker.Checkpoint = func(_ context.Context, _ string) error { return errors.New("lease write failed") }

	next, backoff := worker.pollAndDispatch(context.Background(), "tok-0")
	if next != "tok-0" {
		t.Errorf("next = %q, want the unchanged token %q on a checkpoint error", next, "tok-0")
	}
	if backoff != time.Second {
		t.Errorf("backoff = %v, want 1s on a checkpoint error", backoff)
	}
}

func TestWorker_CheckpointRunsOnDetachedContext(t *testing.T) {
	// Settlement outlives cancellation: a checkpoint fired after a successful dispatch must still run
	// even if the run's context is already cancelled, so handled progress is persisted rather than lost.
	reader := &fakeReader{results: []readResult{{page: docsPage("tok-1", "One")}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checkpointErr := errors.New("sentinel")
	var sawCtxErr error
	worker := newRunnableWorker(t, reader)
	worker.Checkpoint = func(cpCtx context.Context, _ string) error {
		sawCtxErr = cpCtx.Err()
		return checkpointErr
	}

	// Even with a cancelled parent ctx the dispatch+checkpoint still run (the checkpoint sees a live,
	// detached context), and the checkpoint error keeps the token from advancing.
	next, _ := worker.pollAndDispatch(ctx, "tok-0")
	if sawCtxErr != nil {
		t.Errorf("checkpoint context error = %v, want nil (the checkpoint must run on a detached context)", sawCtxErr)
	}
	if next != "tok-0" {
		t.Errorf("next = %q, want the unchanged token after the checkpoint error", next)
	}
}

func TestWorker_RunDrainsThenPacesThenCancels(t *testing.T) {
	// Two document pages drain back-to-back (backoff 0 between them), then an empty page paces with an
	// injected sleep that cancels the run. The token threads across iterations: page 1 starts from
	// StartContinuation, page 2 from page 1's token, the empty poll from page 2's token.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &fakeReader{
		results: []readResult{
			{page: docsPage("tok-1", "One")},
			{page: docsPage("tok-2", "Two")},
			{page: ChangeFeedPage{Continuation: "tok-3"}},
		},
	}
	var checkpointed []string
	var sleptFor time.Duration
	worker := newRunnableWorker(t, reader)
	worker.StartContinuation = "tok-0"
	worker.Checkpoint = func(_ context.Context, cont string) error { checkpointed = append(checkpointed, cont); return nil }
	worker.sleep = func(_ context.Context, d time.Duration) { sleptFor = d; cancel() }

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	wantConts := []string{"tok-0", "tok-1", "tok-2"}
	if len(reader.seenConts) < 3 {
		t.Fatalf("reader saw continuations %v, want at least %v", reader.seenConts, wantConts)
	}
	for i, want := range wantConts {
		if reader.seenConts[i] != want {
			t.Errorf("read #%d continuation = %q, want %q", i, reader.seenConts[i], want)
		}
	}
	if len(checkpointed) != 2 {
		t.Errorf("checkpointed %v, want 2 tokens (the two document pages)", checkpointed)
	}
	if sleptFor != 5*time.Second {
		t.Errorf("paced sleep = %v, want the default PollInterval 5s on the caught-up page", sleptFor)
	}
}

func TestWorker_RunBacksOffOnReaderErrorWithInjectedSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &fakeReader{results: []readResult{{err: errors.New("transient")}}, cancel: cancel}
	var sleptFor time.Duration
	sleepCalls := 0
	worker := newRunnableWorker(t, reader)
	worker.MaxBatchSize = 25
	worker.ErrorBackoff = 500 * time.Millisecond
	worker.sleep = func(_ context.Context, d time.Duration) {
		sleepCalls++
		sleptFor = d
		cancel() // end the loop after backing off once
	}

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled after backoff", err)
	}
	if sleepCalls != 1 {
		t.Errorf("sleep called %d times, want 1", sleepCalls)
	}
	if sleptFor != 500*time.Millisecond {
		t.Errorf("backoff = %v, want 500ms (the configured ErrorBackoff)", sleptFor)
	}
	if reader.lastMax != 25 {
		t.Errorf("ReadNext maxItems = %d, want 25 (the configured MaxBatchSize)", reader.lastMax)
	}
}

func TestWorker_RunBacksOffWithDefaultsAndRealSleep(t *testing.T) {
	// No injected sleep and no configured backoff/batch: exercise the default MaxBatchSize (100),
	// default ErrorBackoff (1s), and the real sleepContext - the fake cancels the context as it returns
	// the error, so sleepContext returns immediately via ctx.Done rather than waiting 1s.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &fakeReader{results: []readResult{{err: errors.New("transient")}}, cancel: cancel, cancelOnErr: true}
	worker := newRunnableWorker(t, reader)

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if reader.lastMax != 100 {
		t.Errorf("ReadNext maxItems = %d, want the default 100", reader.lastMax)
	}
}

func TestWorker_RunReturnsOnAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &fakeReader{}
	worker := newRunnableWorker(t, reader)

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if reader.calls != 0 {
		t.Errorf("ReadNext called %d times, want 0 for an already-cancelled context", reader.calls)
	}
}

func TestWorker_RunReturnsWhenContextCancelledDuringBackoff(t *testing.T) {
	// The reader keeps erroring; the injected sleep does NOT cancel, but the context is cancelled out
	// from under the loop after the first backoff, so Run must observe it and return on the next pass.
	ctx, cancel := context.WithCancel(context.Background())
	reader := &fakeReader{results: []readResult{{err: errors.New("e1")}, {err: errors.New("e2")}}}
	sleepCalls := 0
	worker := newRunnableWorker(t, reader)
	worker.sleep = func(context.Context, time.Duration) {
		sleepCalls++
		cancel() // cancel during the backoff; the loop's next ctx.Err() check must return
	}

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if sleepCalls != 1 {
		t.Errorf("sleep called %d times, want 1 (loop returns on the cancel it observed)", sleepCalls)
	}
}

func TestWorker_RunReturnsWhenContextCancelledBeforeBackoffSleep(t *testing.T) {
	// A reader error whose context is already cancelled when the cycle returns: Run must take the
	// "ctx.Err() != nil" fast path and return WITHOUT sleeping.
	ctx, cancel := context.WithCancel(context.Background())
	reader := &fakeReader{results: []readResult{{err: errors.New("boom")}}, cancel: cancel, cancelOnErr: true}
	sleepCalls := 0
	worker := newRunnableWorker(t, reader)
	worker.sleep = func(context.Context, time.Duration) { sleepCalls++ }

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if sleepCalls != 0 {
		t.Errorf("sleep called %d times, want 0 (cancel observed before the backoff sleep)", sleepCalls)
	}
}

func TestWorker_Validate(t *testing.T) {
	builder := newTestBuilder(t)
	tests := []struct {
		name    string
		worker  *Worker
		wantErr bool
	}{
		{name: "runnable", worker: &Worker{Reader: &fakeReader{}, Builder: builder, Topic: benzene.NewTopic(changedTopic)}, wantErr: false},
		{name: "missing reader", worker: &Worker{Builder: builder, Topic: benzene.NewTopic(changedTopic)}, wantErr: true},
		{name: "missing builder", worker: &Worker{Reader: &fakeReader{}, Topic: benzene.NewTopic(changedTopic)}, wantErr: true},
		{name: "missing topic", worker: &Worker{Reader: &fakeReader{}, Builder: builder}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.worker.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestWorker_Defaults(t *testing.T) {
	if got := (&Worker{}).maxBatchSize(); got != 100 {
		t.Errorf("maxBatchSize() default = %d, want 100", got)
	}
	if got := (&Worker{MaxBatchSize: 5}).maxBatchSize(); got != 5 {
		t.Errorf("maxBatchSize() configured = %d, want 5", got)
	}
	if got := (&Worker{}).pollInterval(); got != 5*time.Second {
		t.Errorf("pollInterval() default = %v, want 5s", got)
	}
	if got := (&Worker{PollInterval: 2 * time.Second}).pollInterval(); got != 2*time.Second {
		t.Errorf("pollInterval() configured = %v, want 2s", got)
	}
	if got := (&Worker{}).errorBackoff(); got != time.Second {
		t.Errorf("errorBackoff() default = %v, want 1s", got)
	}
	if got := (&Worker{ErrorBackoff: 3 * time.Second}).errorBackoff(); got != 3*time.Second {
		t.Errorf("errorBackoff() configured = %v, want 3s", got)
	}
}

func TestWorker_SleepFor(t *testing.T) {
	// sleepFor with no injected sleep uses the real sleepContext (returns at once on a cancelled ctx).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&Worker{}).sleepFor(ctx, time.Hour)
	// sleepFor with an injected sleep uses it.
	called := false
	(&Worker{sleep: func(context.Context, time.Duration) { called = true }}).sleepFor(context.Background(), time.Second)
	if !called {
		t.Error("sleepFor should call the injected sleep function")
	}
}

func TestSleepContext(t *testing.T) {
	// ctx.Done branch: an already-cancelled context returns immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sleepContext(ctx, time.Hour)
	// timer.C branch: a short duration elapses.
	sleepContext(context.Background(), time.Millisecond)
}

func TestBuildBatch(t *testing.T) {
	headers, body := buildBatch([]json.RawMessage{json.RawMessage(`{"id":"a"}`), json.RawMessage(`{"id":"b"}`)})
	if headers["cosmos-document-count"] != "2" {
		t.Errorf("cosmos-document-count = %q, want %q", headers["cosmos-document-count"], "2")
	}
	if body != `[{"id":"a"},{"id":"b"}]` {
		t.Errorf("body = %q, want the JSON array of the documents", body)
	}
	// Empty batch marshals to an empty array with a zero count.
	headers, body = buildBatch(nil)
	if headers["cosmos-document-count"] != "0" || body != "[]" {
		t.Errorf("empty batch = (%q, %q), want (\"0\", \"[]\")", headers["cosmos-document-count"], body)
	}
	// Defensive marshal-error branch: an invalid json.RawMessage cannot be re-marshalled (Cosmos never
	// returns invalid JSON, so this is unreachable in practice) - it degrades to "[]", not a panic.
	headers, body = buildBatch([]json.RawMessage{json.RawMessage("{not json")})
	if headers["cosmos-document-count"] != "1" || body != "[]" {
		t.Errorf("invalid-document batch = (%q, %q), want (\"1\", \"[]\")", headers["cosmos-document-count"], body)
	}
}
