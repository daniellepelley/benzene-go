package azureeventhub

import (
	"context"
	"errors"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

// fakeReceiver replays a scripted sequence of ReceiveEvents results; once drained it cancels the
// run's context and reports cancellation, the shape a real receiver takes when its context is
// cancelled with nothing pending. Everything runs on Run's own goroutine, so no locking is needed.
type fakeReceiver struct {
	results     []receiveResult
	idx         int
	calls       int
	lastMax     int
	cancel      context.CancelFunc
	cancelOnErr bool
}

type receiveResult struct {
	events []ReceivedEvent
	err    error
}

func (f *fakeReceiver) ReceiveEvents(_ context.Context, max int) ([]ReceivedEvent, error) {
	f.calls++
	f.lastMax = max
	if f.idx >= len(f.results) {
		if f.cancel != nil {
			f.cancel()
		}
		return nil, context.Canceled
	}
	r := f.results[f.idx]
	f.idx++
	if r.err != nil && f.cancelOnErr && f.cancel != nil {
		f.cancel()
	}
	return r.events, r.err
}

func topicEvent(name string) ReceivedEvent {
	return ReceivedEvent{
		Properties:     map[string]any{"topic": "greet"},
		Body:           []byte(`{"name":"` + name + `"}`),
		SequenceNumber: 7,
		Checkpoint:     "token",
	}
}

func TestConsumer_DispatchesAndCheckpoints(t *testing.T) {
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{topicEvent("One"), topicEvent("Two")}}}}
	var checkpointed []ReceivedEvent
	consumer := &Consumer{
		Receiver:   receiver,
		Builder:    newTestBuilder(t),
		Checkpoint: func(_ context.Context, e ReceivedEvent) error { checkpointed = append(checkpointed, e); return nil },
	}

	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("receiveAndDispatch() error = %v", err)
	}
	if len(checkpointed) != 2 {
		t.Errorf("checkpointed %d events, want 2", len(checkpointed))
	}
	if len(checkpointed) == 2 && checkpointed[0].SequenceNumber != 7 {
		t.Errorf("checkpoint got SequenceNumber %d, want 7", checkpointed[0].SequenceNumber)
	}
}

func TestConsumer_FailureInvokesOnFailureAndDoesNotCheckpoint(t *testing.T) {
	tests := []struct {
		name       string
		event      ReceivedEvent
		wantStatus benzene.Status
	}{
		{name: "handler failure status", event: topicEvent(""), wantStatus: benzene.StatusBadRequest},
		{
			name:       "no topic property and un-parseable body yields no route",
			event:      ReceivedEvent{Body: []byte("just some text")},
			wantStatus: benzene.StatusValidationError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{tt.event}}}}
			var failed []wire.Response
			checkpointCalls := 0
			consumer := &Consumer{
				Receiver: receiver,
				Builder:  newTestBuilder(t),
				OnFailure: func(_ context.Context, _ ReceivedEvent, resp wire.Response) {
					failed = append(failed, resp)
				},
				Checkpoint: func(_ context.Context, _ ReceivedEvent) error { checkpointCalls++; return nil },
			}

			if err := consumer.receiveAndDispatch(context.Background()); err != nil {
				t.Fatalf("receiveAndDispatch() error = %v", err)
			}
			if len(failed) != 1 {
				t.Fatalf("OnFailure called %d times, want 1", len(failed))
			}
			if failed[0].StatusCode != string(tt.wantStatus) {
				t.Errorf("failed StatusCode = %q, want %q", failed[0].StatusCode, tt.wantStatus)
			}
			if checkpointCalls != 0 {
				t.Errorf("checkpoint called %d times on failure, want 0", checkpointCalls)
			}
		})
	}
}

func TestConsumer_StopsAtFirstFailureInBatch(t *testing.T) {
	// Ordered-stream guarantee: a failing event[0] must NOT let a later successful event[1] checkpoint
	// past it (which would silently drop event[0]). The Consumer stops at the first failure - OnFailure
	// once, no checkpoint at all - so the durable position never advances beyond the unhandled event.
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{topicEvent(""), topicEvent("Two")}}}}
	var failed []wire.Response
	var checkpointed []ReceivedEvent
	consumer := &Consumer{
		Receiver:   receiver,
		Builder:    newTestBuilder(t),
		OnFailure:  func(_ context.Context, _ ReceivedEvent, resp wire.Response) { failed = append(failed, resp) },
		Checkpoint: func(_ context.Context, e ReceivedEvent) error { checkpointed = append(checkpointed, e); return nil },
	}

	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("receiveAndDispatch() error = %v", err)
	}
	if len(failed) != 1 {
		t.Errorf("OnFailure called %d times, want 1 (stop at the first failure)", len(failed))
	}
	if len(checkpointed) != 0 {
		t.Errorf("checkpointed %d events, want 0 - the successful event[1] must not advance past the failed event[0]", len(checkpointed))
	}
}

func TestConsumer_InheritsBuilderReservedNames(t *testing.T) {
	// A service that renamed its topic key on the builder must route on the Event Hub consumer too,
	// without setting ReservedNames on the Consumer - the consumer inherits the builder's names.
	builder := newTestBuilder(t)
	builder.ReservedNames = wire.ReservedNames{TopicKey: "x-my-topic"}
	event := ReceivedEvent{Properties: map[string]any{"x-my-topic": "greet"}, Body: []byte(`{"name":"World"}`)}
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{event}}}}
	checkpointCalls := 0
	consumer := &Consumer{
		Receiver:   receiver,
		Builder:    builder,
		Checkpoint: func(_ context.Context, _ ReceivedEvent) error { checkpointCalls++; return nil },
	}

	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("receiveAndDispatch() error = %v", err)
	}
	if checkpointCalls != 1 {
		t.Errorf("checkpoint called %d times, want 1 - the inherited topic key should route the event", checkpointCalls)
	}
	// An explicit override on the Consumer still wins over the builder's names.
	if got := (&Consumer{Builder: builder, ReservedNames: wire.ReservedNames{TopicKey: "override"}}).topicKey(); got != "override" {
		t.Errorf("topicKey() = %q, want the explicit override %q", got, "override")
	}
}

func TestConsumer_EnvelopeFallbackDispatches(t *testing.T) {
	// No topic property: the body is a full wire envelope, so the topic is lifted from it.
	envelopeBody := `{"topic":"greet","headers":{"x-correlation-id":"abc"},"body":"{\"name\":\"World\"}"}`
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{{Body: []byte(envelopeBody)}}}}}
	checkpointCalls := 0
	consumer := &Consumer{
		Receiver:   receiver,
		Builder:    newTestBuilder(t),
		Checkpoint: func(_ context.Context, _ ReceivedEvent) error { checkpointCalls++; return nil },
	}

	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("receiveAndDispatch() error = %v", err)
	}
	if checkpointCalls != 1 {
		t.Errorf("checkpoint called %d times, want 1 (envelope-body topic should route and succeed)", checkpointCalls)
	}
}

func TestConsumer_ReceiveErrorPropagatesFromCycle(t *testing.T) {
	wantErr := errors.New("hub gone")
	receiver := &fakeReceiver{results: []receiveResult{{err: wantErr}}}
	consumer := &Consumer{Receiver: receiver, Builder: newTestBuilder(t)}

	if err := consumer.receiveAndDispatch(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("receiveAndDispatch() error = %v, want %v", err, wantErr)
	}
}

func TestConsumer_CheckpointErrorPropagatesFromCycle(t *testing.T) {
	wantErr := errors.New("blob write failed")
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{topicEvent("One")}}}}
	consumer := &Consumer{
		Receiver:   receiver,
		Builder:    newTestBuilder(t),
		Checkpoint: func(_ context.Context, _ ReceivedEvent) error { return wantErr },
	}

	if err := consumer.receiveAndDispatch(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("receiveAndDispatch() error = %v, want %v", err, wantErr)
	}
}

func TestConsumer_RunBacksOffOnReceiveErrorWithInjectedSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiver := &fakeReceiver{results: []receiveResult{{err: errors.New("transient")}}, cancel: cancel}
	var sleptFor time.Duration
	sleepCalls := 0
	consumer := &Consumer{
		Receiver:     receiver,
		Builder:      newTestBuilder(t),
		MaxBatchSize: 25,
		ErrorBackoff: 500 * time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) {
			sleepCalls++
			sleptFor = d
			cancel() // end the loop after backing off once
		},
	}

	if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled after backoff", err)
	}
	if sleepCalls != 1 {
		t.Errorf("sleep called %d times, want 1", sleepCalls)
	}
	if sleptFor != 500*time.Millisecond {
		t.Errorf("backoff = %v, want 500ms (the configured ErrorBackoff)", sleptFor)
	}
	if receiver.lastMax != 25 {
		t.Errorf("ReceiveEvents max = %d, want 25 (the configured MaxBatchSize)", receiver.lastMax)
	}
}

func TestConsumer_RunBacksOffWithDefaultsAndRealSleep(t *testing.T) {
	// No injected sleep and no configured backoff/batch: exercise the default MaxBatchSize (100),
	// default ErrorBackoff (1s), and the real sleepContext - the fake cancels the context as it
	// returns the error, so sleepContext returns immediately via ctx.Done rather than waiting 1s.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiver := &fakeReceiver{results: []receiveResult{{err: errors.New("transient")}}, cancel: cancel, cancelOnErr: true}
	consumer := &Consumer{Receiver: receiver, Builder: newTestBuilder(t)}

	if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if receiver.lastMax != 100 {
		t.Errorf("ReceiveEvents max = %d, want the default 100", receiver.lastMax)
	}
}

func TestConsumer_RunReturnsOnAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receiver := &fakeReceiver{}
	consumer := &Consumer{Receiver: receiver, Builder: newTestBuilder(t)}

	if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if receiver.calls != 0 {
		t.Errorf("ReceiveEvents called %d times, want 0 for an already-cancelled context", receiver.calls)
	}
}

func TestConsumer_RunCleanDrainReturnsCancelled(t *testing.T) {
	// A drained receiver cancels the context and reports cancellation; Run treats that as shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{topicEvent("One")}}}, cancel: cancel}
	checkpointCalls := 0
	consumer := &Consumer{
		Receiver:   receiver,
		Builder:    newTestBuilder(t),
		Checkpoint: func(_ context.Context, _ ReceivedEvent) error { checkpointCalls++; return nil },
	}

	if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled after the drain", err)
	}
	if checkpointCalls != 1 {
		t.Errorf("checkpoint called %d times, want 1 (the one event before the drain)", checkpointCalls)
	}
}

func TestConsumer_Validate(t *testing.T) {
	builder := newTestBuilder(t)
	tests := []struct {
		name     string
		consumer *Consumer
		wantErr  bool
	}{
		{name: "runnable", consumer: &Consumer{Receiver: &fakeReceiver{}, Builder: builder}, wantErr: false},
		{name: "missing receiver", consumer: &Consumer{Builder: builder}, wantErr: true},
		{name: "missing builder", consumer: &Consumer{Receiver: &fakeReceiver{}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.consumer.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveEvent(t *testing.T) {
	tests := []struct {
		name        string
		event       ReceivedEvent
		topicKey    string
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name: "topic property routes; other string properties become headers",
			event: ReceivedEvent{
				Properties: map[string]any{
					"topic":            "greet",
					"x-correlation-id": "abc",
				},
				Body: []byte(`{"name":"World"}`),
			},
			topicKey:    "topic",
			wantTopic:   "greet",
			wantBody:    `{"name":"World"}`,
			wantHeaders: map[string]string{"x-correlation-id": "abc"},
		},
		{
			name: "non-string property values are ignored",
			event: ReceivedEvent{
				Properties: map[string]any{
					"topic":   "greet",
					"attempt": 3,
					"flag":    true,
				},
				Body: []byte("{}"),
			},
			topicKey:    "topic",
			wantTopic:   "greet",
			wantBody:    "{}",
			wantHeaders: map[string]string{},
		},
		{
			name: "configured topic key routes",
			event: ReceivedEvent{
				Properties: map[string]any{"x-my-topic": "greet"},
				Body:       []byte("{}"),
			},
			topicKey:    "x-my-topic",
			wantTopic:   "greet",
			wantBody:    "{}",
			wantHeaders: map[string]string{},
		},
		{
			name: "no topic property: body parsed as a full wire envelope",
			event: ReceivedEvent{
				Properties: map[string]any{"x-correlation-id": "abc"},
				Body:       []byte(`{"topic":"greet","headers":{"trace":"1"},"body":"{}"}`),
			},
			topicKey:    "topic",
			wantTopic:   "greet",
			wantBody:    "{}",
			wantHeaders: map[string]string{"x-correlation-id": "abc", "trace": "1"},
		},
		{
			name:        "no topic property and non-envelope body yields empty topic",
			event:       ReceivedEvent{Body: []byte("plain text")},
			topicKey:    "topic",
			wantTopic:   "",
			wantBody:    "plain text",
			wantHeaders: map[string]string{},
		},
		{
			name:        "no properties is not confused with nil headers",
			event:       ReceivedEvent{Properties: map[string]any{"topic": "greet"}},
			topicKey:    "topic",
			wantTopic:   "greet",
			wantBody:    "",
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := resolveEvent(tt.event, tt.topicKey)
			if req.Topic != tt.wantTopic {
				t.Errorf("Topic = %q, want %q", req.Topic, tt.wantTopic)
			}
			if req.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q", req.Body, tt.wantBody)
			}
			if len(req.Headers) != len(tt.wantHeaders) {
				t.Fatalf("Headers = %v, want %v", req.Headers, tt.wantHeaders)
			}
			for k, v := range tt.wantHeaders {
				if req.Headers[k] != v {
					t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
				}
			}
		})
	}
}

func TestConsumer_DefaultsAndSleepFor(t *testing.T) {
	// errorBackoff: default vs configured.
	if got := (&Consumer{}).errorBackoff(); got != time.Second {
		t.Errorf("errorBackoff() default = %v, want 1s", got)
	}
	if got := (&Consumer{ErrorBackoff: 2 * time.Second}).errorBackoff(); got != 2*time.Second {
		t.Errorf("errorBackoff() configured = %v, want 2s", got)
	}
	// maxBatchSize: default vs configured.
	if got := (&Consumer{}).maxBatchSize(); got != 100 {
		t.Errorf("maxBatchSize() default = %d, want 100", got)
	}
	if got := (&Consumer{MaxBatchSize: 5}).maxBatchSize(); got != 5 {
		t.Errorf("maxBatchSize() configured = %d, want 5", got)
	}
	// sleepFor with no injected sleep uses the real sleepContext (returns at once on a cancelled ctx).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&Consumer{}).sleepFor(ctx, time.Hour)
	// sleepFor with an injected sleep uses it.
	called := false
	(&Consumer{sleep: func(context.Context, time.Duration) { called = true }}).sleepFor(context.Background(), time.Second)
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
