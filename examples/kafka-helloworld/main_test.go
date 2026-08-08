package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/kafka"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"
)

// These tests boot the real app from its composition root (newApp) via benzenetest and push a
// native Kafka record in the front door - the same shape every transport's example uses. Kafka has
// no benzenetest.Send* helper, so the front door here is a fake kafka.MessageSource feeding one
// record into the real Consumer (which runs the real pipeline + DI scope per record); only the
// transport plumbing is faked, the app and assertions read like helloworld's. A spy Greeter, injected
// via WithServices, captures what the handler actually did, since a Kafka consumer returns no response.

// spyGreeter records every greeting, so a test can assert the handler ran with the routed message.
type spyGreeter struct {
	mu      sync.Mutex
	greeted []string
}

func (g *spyGreeter) Greet(name string) string {
	greeting := "Hello, " + name + "!"
	g.mu.Lock()
	g.greeted = append(g.greeted, greeting)
	g.mu.Unlock()
	return greeting
}

func (g *spyGreeter) recorded() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.greeted...)
}

// fakeSource feeds a fixed slice of records, then cancels the run's context and reports the
// cancellation - the shape a real reader takes when its context is cancelled with nothing left to
// fetch. It records every committed message so a test can assert the consumer acknowledged.
type fakeSource struct {
	messages  []kafkago.Message
	committed []kafkago.Message
	cancel    context.CancelFunc
}

func (s *fakeSource) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	if len(s.messages) == 0 {
		s.cancel()
		return kafkago.Message{}, context.Canceled
	}
	msg := s.messages[0]
	s.messages = s.messages[1:]
	return msg, nil
}

func (s *fakeSource) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	s.committed = append(s.committed, msgs...)
	return nil
}

func greetRecord(t *testing.T, name string) kafkago.Message {
	t.Helper()
	body, err := json.Marshal(greetRequest{Name: name})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return kafkago.Message{Topic: greetTopic, Value: body}
}

// runConsumer drives the consumer over source until source has fed every record, at which point
// source cancels the context and Run's clean-shutdown path returns nil.
func runConsumer(t *testing.T, consumer *kafka.Consumer, source *fakeSource) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source.cancel = cancel
	if err := consumer.Run(ctx); err != nil {
		t.Fatalf("consumer.Run() error = %v", err)
	}
}

func hostWithSpy(spy *spyGreeter) *benzenetest.Host {
	return benzenetest.NewHost(newApp(), benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
		benzene.AddSingleton(b.Container, greeterKey, func(_ *benzene.Scope) Greeter { return spy })
	}))
}

func TestConsumer_DispatchesRecordToGreetHandler(t *testing.T) {
	spy := &spyGreeter{}
	host := hostWithSpy(spy)
	source := &fakeSource{messages: []kafkago.Message{greetRecord(t, "World"), greetRecord(t, "Go")}}

	runConsumer(t, newConsumer(host.Builder(), source), source)

	got := spy.recorded()
	if len(got) != 2 || got[0] != "Hello, World!" || got[1] != "Hello, Go!" {
		t.Errorf("recorded greetings = %v, want [Hello, World! Hello, Go!]", got)
	}
	if len(source.committed) != 2 {
		t.Errorf("committed %d records, want 2", len(source.committed))
	}
}

func TestConsumer_FailedRecordStillCommitsAndReportsFailure(t *testing.T) {
	spy := &spyGreeter{}
	host := hostWithSpy(spy)
	source := &fakeSource{messages: []kafkago.Message{greetRecord(t, "")}} // empty name -> BadRequest

	var failed bool
	consumer := newConsumer(host.Builder(), source)
	consumer.OnFailure = func(context.Context, kafkago.Message, wire.Response) { failed = true }

	runConsumer(t, consumer, source)

	if !failed {
		t.Error("OnFailure was not called for a failed dispatch")
	}
	if len(source.committed) != 1 {
		t.Errorf("committed %d records, want 1 - Kafka commits failures too (no broker redelivery)", len(source.committed))
	}
}

// fakeWriter captures published messages so the publish path can be asserted with no live broker.
type fakeWriter struct {
	written []kafkago.Message
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	w.written = append(w.written, msgs...)
	return nil
}

func TestClient_PublishesToTheGreetTopic(t *testing.T) {
	writer := &fakeWriter{}
	client := kafka.NewClient(writer)

	body, err := json.Marshal(greetRequest{Name: "World"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	result := client.Send(context.Background(), benzene.NewTopic(greetTopic), nil, body)

	if result.Status != benzene.StatusAccepted {
		t.Errorf("status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if len(writer.written) != 1 || writer.written[0].Topic != greetTopic {
		t.Fatalf("written = %+v, want one message on topic %q", writer.written, greetTopic)
	}
}
