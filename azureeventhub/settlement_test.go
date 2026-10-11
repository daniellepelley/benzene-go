package azureeventhub

import (
	"context"
	"errors"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// flakyBuilder serves "greet" with a handler that fails the first time it sees "Flaky" and succeeds
// for everything else, recording the order it ran in.
func flakyBuilder(t *testing.T, ran *[]string) *benzene.ApplicationBuilder {
	t.Helper()
	failed := false
	registry := benzene.NewRegistry()
	benzene.MustRegister(registry, benzene.NewTopic("greet"), func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
		*ran = append(*ran, req.Name)
		if req.Name == "Flaky" && !failed {
			failed = true
			return benzene.ServiceUnavailable[greetResponse]("downstream down")
		}
		return benzene.Ok(greetResponse{Greeting: "hi"})
	})
	return &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
}

// The bug this pins: the SDK partition receiver has already moved past a whole received batch, so
// receiving again after a failure used to skip the failed event (and the rest of its batch) for
// good, and the next success's checkpoint moved the durable position past it. The Consumer now
// re-dispatches the held remainder before it receives anything new.
func TestConsumer_RedeliversAFailedEventInARunningProcess(t *testing.T) {
	var ran []string
	var checkpointed []string
	receiver := &fakeReceiver{results: []receiveResult{
		{events: []ReceivedEvent{topicEvent("Flaky"), topicEvent("Two")}},
		{events: []ReceivedEvent{topicEvent("Three")}},
	}}
	consumer := &Consumer{
		Receiver: receiver,
		Builder:  flakyBuilder(t, &ran),
		Checkpoint: func(_ context.Context, e ReceivedEvent) error {
			checkpointed = append(checkpointed, string(e.Body))
			return nil
		},
	}

	if err := consumer.receiveAndDispatch(context.Background()); !errors.Is(err, errRedeliver) {
		t.Fatalf("cycle 1 error = %v, want errRedeliver", err)
	}
	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("cycle 2 error = %v, want nil (the redelivered event succeeds)", err)
	}
	if receiver.calls != 1 {
		t.Fatalf("ReceiveEvents called %d times before the held batch settled, want 1", receiver.calls)
	}
	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("cycle 3 error = %v", err)
	}

	if want := []string{"Flaky", "Flaky", "Two", "Three"}; !equal(ran, want) {
		t.Errorf("dispatch order = %v, want %v", ran, want)
	}
	if want := []string{`{"name":"Flaky"}`, `{"name":"Two"}`, `{"name":"Three"}`}; !equal(checkpointed, want) {
		t.Errorf("checkpoints = %v, want each event once, in order: %v", checkpointed, want)
	}
}

func TestConsumer_RunBacksOffBeforeRedelivering(t *testing.T) {
	var ran []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{topicEvent("Flaky")}}}, cancel: cancel}
	var slept []time.Duration
	consumer := &Consumer{
		Receiver:     receiver,
		Builder:      flakyBuilder(t, &ran),
		ErrorBackoff: 3 * time.Second,
		sleep:        func(_ context.Context, d time.Duration) { slept = append(slept, d) },
	}

	if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled once the receiver drains", err)
	}
	if !equal(ran, []string{"Flaky", "Flaky"}) || len(slept) == 0 || slept[0] != 3*time.Second {
		t.Errorf("ran %v after backoffs %v, want the failed event redelivered after ErrorBackoff", ran, slept)
	}
}

func TestConsumer_CheckpointErrorKeepsTheRestOfTheBatch(t *testing.T) {
	var ran []string
	calls := 0
	receiver := &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{topicEvent("One"), topicEvent("Two")}}}}
	consumer := &Consumer{
		Receiver: receiver,
		Builder:  flakyBuilder(t, &ran),
		Checkpoint: func(context.Context, ReceivedEvent) error {
			calls++
			if calls == 1 {
				return errors.New("checkpoint store down")
			}
			return nil
		},
	}

	if err := consumer.receiveAndDispatch(context.Background()); err == nil || errors.Is(err, errRedeliver) {
		t.Fatalf("cycle 1 error = %v, want the checkpoint error", err)
	}
	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("cycle 2 error = %v", err)
	}
	if !equal(ran, []string{"One", "Two"}) || receiver.calls != 1 {
		t.Errorf("ran %v with %d receives, want Two dispatched from the held batch, not lost", ran, receiver.calls)
	}
}

func TestConsumer_Settlement(t *testing.T) {
	batch := func() *fakeReceiver {
		return &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{
			topicEvent(""), // a handler failure
			{Properties: map[string]any{"topic": "someone:elses"}, Body: []byte(`{}`)},
		}}}}
	}

	var checkpoints, failures int
	consumer := &Consumer{
		Receiver:   batch(),
		Builder:    newTestBuilder(t),
		Settlement: envelope.Settlement{Delivery: envelope.AtMostOnce},
		OnFailure:  func(context.Context, ReceivedEvent, wire.Response) { failures++ },
		Checkpoint: func(context.Context, ReceivedEvent) error { checkpoints++; return nil },
	}
	if err := consumer.receiveAndDispatch(context.Background()); err != nil {
		t.Fatalf("at-most-once error = %v, want nil", err)
	}
	if checkpoints != 2 || failures != 2 {
		t.Errorf("at-most-once checkpointed %d with %d failures reported, want both skipped past and both reported", checkpoints, failures)
	}

	checkpoints = 0
	consumer = &Consumer{
		Receiver:   batch(),
		Builder:    newTestBuilder(t),
		Settlement: envelope.Settlement{IgnoreUnmatchedTopics: true},
		Checkpoint: func(context.Context, ReceivedEvent) error { checkpoints++; return nil },
	}
	if err := consumer.receiveAndDispatch(context.Background()); !errors.Is(err, errRedeliver) || checkpoints != 0 {
		t.Errorf("IgnoreUnmatchedTopics = %v after %d checkpoints, want the handler failure held first", err, checkpoints)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
