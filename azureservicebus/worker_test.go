package azureservicebus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// fakeReceiver is an in-memory ReceiverAPI: it hands out one scripted batch per ReceiveMessages call
// and records how each message was settled, with an injectable receive error, a per-call hook, and an
// injectable complete error.
type fakeReceiver struct {
	batches     [][]*azservicebus.ReceivedMessage
	recvErr     error
	completeErr error
	recvCalls   int
	onReceive   func(call int)

	completed    []string
	abandoned    []string
	deadLettered []string
	lastDeadOpts *azservicebus.DeadLetterOptions
	settleCtxErr error // ctx.Err() observed inside CompleteMessage
}

func (f *fakeReceiver) ReceiveMessages(_ context.Context, _ int, _ *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error) {
	call := f.recvCalls
	f.recvCalls++
	if f.onReceive != nil {
		f.onReceive(call)
	}
	if f.recvErr != nil {
		return nil, f.recvErr
	}
	if call < len(f.batches) {
		return f.batches[call], nil
	}
	return nil, nil
}

func (f *fakeReceiver) CompleteMessage(ctx context.Context, message *azservicebus.ReceivedMessage, _ *azservicebus.CompleteMessageOptions) error {
	f.settleCtxErr = ctx.Err()
	if f.completeErr != nil {
		return f.completeErr
	}
	f.completed = append(f.completed, message.MessageID)
	return nil
}

func (f *fakeReceiver) AbandonMessage(_ context.Context, message *azservicebus.ReceivedMessage, _ *azservicebus.AbandonMessageOptions) error {
	f.abandoned = append(f.abandoned, message.MessageID)
	return nil
}

func (f *fakeReceiver) DeadLetterMessage(_ context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.DeadLetterOptions) error {
	f.deadLettered = append(f.deadLettered, message.MessageID)
	f.lastDeadOpts = options
	return nil
}

func received(id, topic, body string) *azservicebus.ReceivedMessage {
	m := &azservicebus.ReceivedMessage{MessageID: id, Body: []byte(body)}
	if topic != "" {
		m.ApplicationProperties = map[string]any{"topic": topic}
	}
	return m
}

func TestWorker_PollDispatchesAndCompletesSuccessful(t *testing.T) {
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{received("m-1", "greet", `{"name":"World"}`)}}}
	w := NewWorker(api, newTestBuilder(t))

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.completed) != 1 || api.completed[0] != "m-1" {
		t.Errorf("completed = %v, want [m-1] (successful dispatch is completed)", api.completed)
	}
	if len(api.abandoned) != 0 || len(api.deadLettered) != 0 {
		t.Errorf("a successful message must not be abandoned/dead-lettered; got abandoned=%v dead=%v", api.abandoned, api.deadLettered)
	}
}

func TestWorker_PollAbandonsAndReportsFailure(t *testing.T) {
	var gotID, gotStatus string
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{received("m-2", "greet", `{"name":""}`)}}}
	w := NewWorker(api, newTestBuilder(t),
		WithOnFailure(func(id string, resp wire.Response) { gotID, gotStatus = id, resp.StatusCode }))

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.abandoned) != 1 || api.abandoned[0] != "m-2" {
		t.Errorf("abandoned = %v, want [m-2] (a failed message is abandoned by default)", api.abandoned)
	}
	if len(api.completed) != 0 {
		t.Errorf("a failed message must not be completed; got %v", api.completed)
	}
	if gotID != "m-2" || gotStatus != string(benzene.StatusBadRequest) {
		t.Errorf("OnFailure got (%q, %q), want (m-2, bad-request)", gotID, gotStatus)
	}
}

func TestWorker_PollDeadLettersFailureUnderDeadLetterMode(t *testing.T) {
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{received("m-3", "greet", `{"name":""}`)}}}
	w := NewWorker(api, newTestBuilder(t),
		WithAckMode(AckModeDeadLetter),
		WithDeadLetter("bad-request", "handler rejected the message"))

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deadLettered) != 1 || api.deadLettered[0] != "m-3" {
		t.Errorf("deadLettered = %v, want [m-3] under AckModeDeadLetter", api.deadLettered)
	}
	if len(api.abandoned) != 0 {
		t.Errorf("must not abandon under AckModeDeadLetter; got %v", api.abandoned)
	}
	if api.lastDeadOpts == nil || api.lastDeadOpts.Reason == nil || *api.lastDeadOpts.Reason != "bad-request" {
		t.Errorf("dead-letter reason not passed through: %+v", api.lastDeadOpts)
	}
	if api.lastDeadOpts.ErrorDescription == nil || *api.lastDeadOpts.ErrorDescription != "handler rejected the message" {
		t.Errorf("dead-letter description not passed through: %+v", api.lastDeadOpts)
	}
}

func TestWorker_PollMixedBatchSettlesEachIndependently(t *testing.T) {
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{
		received("ok-1", "greet", `{"name":"A"}`),
		received("bad-1", "greet", `{"name":""}`),
		received("ok-2", "greet", `{"name":"B"}`),
	}}}
	w := NewWorker(api, newTestBuilder(t))

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.completed) != 2 || len(api.abandoned) != 1 || api.abandoned[0] != "bad-1" {
		t.Errorf("mixed batch: completed=%v abandoned=%v, want 2 completed + [bad-1] abandoned", api.completed, api.abandoned)
	}
}

func TestWorker_ResolvesTopicFromEnvelopeBodyWhenNoProperty(t *testing.T) {
	// No topic application property: fall back to parsing the body as a full wire envelope.
	body := `{"topic":"greet","headers":{"h":"v"},"body":"{\"name\":\"Env\"}"}`
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{received("env-1", "", body)}}}
	w := NewWorker(api, newTestBuilder(t))

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.completed) != 1 || api.completed[0] != "env-1" {
		t.Errorf("completed = %v, want [env-1] (envelope-body topic fallback)", api.completed)
	}
}

func TestWorker_UnresolvedTopicFailsAndAbandons(t *testing.T) {
	// Neither a topic property nor an envelope body -> empty topic -> router validation-error -> abandon.
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{received("no-1", "", "not json")}}}
	w := NewWorker(api, newTestBuilder(t))

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.abandoned) != 1 || api.abandoned[0] != "no-1" {
		t.Errorf("abandoned = %v, want [no-1] for an unresolved topic", api.abandoned)
	}
}

func TestWorker_NonStringApplicationPropertyIsIgnored(t *testing.T) {
	m := received("ns-1", "greet", `{"name":"World"}`)
	m.ApplicationProperties["retry-count"] = 3 // non-string property must be skipped, not a header
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{m}}}
	w := NewWorker(api, newTestBuilder(t))

	req := resolveMessage(m, "topic")
	if req.Topic != "greet" {
		t.Errorf("Topic = %q, want greet", req.Topic)
	}
	if _, ok := req.Headers["retry-count"]; ok {
		t.Error("a non-string application property must not become a header")
	}
	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.completed) != 1 {
		t.Errorf("completed = %v, want the message handled despite a non-string property", api.completed)
	}
}

func TestWorker_HonorsBuilderReservedNames(t *testing.T) {
	builder := newTestBuilder(t)
	builder.UseReservedNames(wire.ReservedNames{TopicKey: "x-topic"})

	m := &azservicebus.ReceivedMessage{
		MessageID:             "rn-1",
		Body:                  []byte(`{"name":"Custom"}`),
		ApplicationProperties: map[string]any{"x-topic": "greet"},
	}
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{m}}}
	w := NewWorker(api, builder)

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.completed) != 1 || api.completed[0] != "rn-1" {
		t.Errorf("completed = %v, want [rn-1] - the builder's custom topic key must be honored", api.completed)
	}
}

func TestWorker_CompleteErrorOnSuccessIsReported(t *testing.T) {
	var reported []string
	api := &fakeReceiver{
		batches:     [][]*azservicebus.ReceivedMessage{{received("c-1", "greet", `{"name":"A"}`)}},
		completeErr: errors.New("lock lost"),
	}
	w := NewWorker(api, newTestBuilder(t),
		WithOnFailure(func(id string, resp wire.Response) { reported = append(reported, id+":"+resp.StatusCode) }))

	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(reported) != 1 || reported[0] != "c-1:"+string(benzene.StatusServiceUnavailable) {
		t.Errorf("reported = %v, want [c-1:service-unavailable] for a failed complete", reported)
	}
}

func TestWorker_SettlementUsesDetachedContext(t *testing.T) {
	// A cancelled invocation context must not cancel the settle - settlement outlives cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api := &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{received("d-1", "greet", `{"name":"A"}`)}}}
	w := NewWorker(api, newTestBuilder(t))

	if err := w.poll(ctx); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if api.settleCtxErr != nil {
		t.Errorf("settle context was cancelled (%v); settlement must run on a detached context", api.settleCtxErr)
	}
	if len(api.completed) != 1 {
		t.Errorf("completed = %v, want the message settled despite a cancelled invocation context", api.completed)
	}
}

func TestWorker_PollReturnsReceiveError(t *testing.T) {
	wantErr := errors.New("receive failed")
	w := NewWorker(&fakeReceiver{recvErr: wantErr}, newTestBuilder(t))

	if err := w.poll(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("poll() error = %v, want %v", err, wantErr)
	}
}

func TestWorker_RunReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := NewWorker(&fakeReceiver{}, newTestBuilder(t))

	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
}

func TestWorker_RunBacksOffOnReceiveErrorThenExits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeReceiver{recvErr: errors.New("transient")}
	w := NewWorker(api, newTestBuilder(t), WithErrorBackoff(time.Hour))
	// Cancel from within the backoff sleep so Run takes the sleep path once, then exits at the loop top.
	w.sleep = func(_ context.Context, _ time.Duration) { cancel() }

	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if api.recvCalls == 0 {
		t.Error("expected at least one receive attempt before backoff")
	}
}

func TestWorker_RunReturnsCtxErrWhenCancelledDuringReceiveError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeReceiver{recvErr: errors.New("transient")}
	api.onReceive = func(int) { cancel() } // ctx cancelled by the time the error is handled
	w := NewWorker(api, newTestBuilder(t))

	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
}

func TestWorker_OptionsAreApplied(t *testing.T) {
	w := NewWorker(&fakeReceiver{}, newTestBuilder(t),
		WithMaxMessages(3),
		WithErrorBackoff(5*time.Second),
		WithAckMode(AckModeDeadLetter),
		WithDeadLetter("r", "d"),
		WithWorkerReservedNames(wire.ReservedNames{TopicKey: "t"}),
	)
	if w.maxMessages != 3 || w.errorBackoff != 5*time.Second || w.ackMode != AckModeDeadLetter {
		t.Errorf("options not applied: %+v", w)
	}
	if w.deadLetterReason == nil || *w.deadLetterReason != "r" || w.reservedNames.TopicKey != "t" {
		t.Errorf("options not applied: %+v", w)
	}
}

func TestSleepContext(t *testing.T) {
	start := time.Now()
	sleepContext(context.Background(), time.Millisecond)
	if time.Since(start) < time.Millisecond {
		t.Error("sleepContext returned before the duration elapsed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { sleepContext(ctx, time.Hour); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("sleepContext did not return promptly on a cancelled context")
	}
}
