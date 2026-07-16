package mesh

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// captureSender records every batch it is asked to send, optionally blocking until
// released so tests can force queue overflow deterministically.
type captureSender struct {
	mu      sync.Mutex
	topics  []string
	batches []TraceBatch
	block   chan struct{} // when non-nil, Send waits for it to close
}

func (c *captureSender) Send(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
	if c.block != nil {
		<-c.block
	}
	var batch TraceBatch
	if err := json.Unmarshal(message, &batch); err != nil {
		return benzene.BadRequest[json.RawMessage](err.Error())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.topics = append(c.topics, topic.String())
	c.batches = append(c.batches, batch)
	return benzene.Ok(json.RawMessage(`{}`))
}

func (c *captureSender) eventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, batch := range c.batches {
		total += len(batch.Events)
	}
	return total
}

func TestPushExporter_FlushesOnBatchSize(t *testing.T) {
	sender := &captureSender{}
	exporter := NewPushExporter(sender, PushExporterOptions{BatchSize: 2, FlushInterval: time.Hour})
	defer exporter.Close()

	exporter.Export(context.Background(), TraceEvent{Topic: "a", Status: "Ok"})
	exporter.Export(context.Background(), TraceEvent{Topic: "b", Status: "Ok"})

	deadline := time.Now().Add(5 * time.Second)
	for sender.eventCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.batches) != 1 || len(sender.batches[0].Events) != 2 {
		t.Fatalf("batches = %v, want one batch of 2 events", sender.batches)
	}
	if sender.topics[0] != TopicTraces {
		t.Errorf("sent to topic %q, want %q", sender.topics[0], TopicTraces)
	}
	if sender.batches[0].Events[0].Topic != "a" || sender.batches[0].Events[1].Topic != "b" {
		t.Errorf("events = %v, want a then b in order", sender.batches[0].Events)
	}
}

func TestPushExporter_FlushesPartialBatchOnInterval(t *testing.T) {
	sender := &captureSender{}
	exporter := NewPushExporter(sender, PushExporterOptions{BatchSize: 100, FlushInterval: 10 * time.Millisecond})
	defer exporter.Close()

	exporter.Export(context.Background(), TraceEvent{Topic: "a", Status: "Ok"})

	deadline := time.Now().Add(5 * time.Second)
	for sender.eventCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sender.eventCount(); got != 1 {
		t.Fatalf("eventCount() = %d, want 1 flushed by the interval", got)
	}
}

func TestPushExporter_CloseFlushesTheTail(t *testing.T) {
	sender := &captureSender{}
	exporter := NewPushExporter(sender, PushExporterOptions{BatchSize: 100, FlushInterval: time.Hour})

	for i := 0; i < 5; i++ {
		exporter.Export(context.Background(), TraceEvent{Topic: "tail", Status: "Ok"})
	}
	exporter.Close()

	if got := sender.eventCount(); got != 5 {
		t.Errorf("eventCount() after Close = %d, want 5", got)
	}
}

func TestPushExporter_FullBufferDropsInsteadOfBlocking(t *testing.T) {
	release := make(chan struct{})
	sender := &captureSender{block: release}
	exporter := NewPushExporter(sender, PushExporterOptions{BatchSize: 1, FlushInterval: time.Hour, BufferSize: 1})

	// The first event puts the sender loop into a blocked Send; the buffer (size 1) then
	// fills, and everything beyond it must be dropped without Export ever blocking.
	const sent = 20
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < sent; i++ {
			exporter.Export(context.Background(), TraceEvent{Topic: "overflow", Status: "Ok"})
		}
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Export blocked on a full buffer")
	}

	close(release)
	exporter.Close()

	if got := sender.eventCount(); got >= sent {
		t.Errorf("eventCount() = %d, want fewer than %d (overflow must drop)", got, sent)
	}
	if got := sender.eventCount(); got == 0 {
		t.Error("eventCount() = 0, want the non-dropped events delivered")
	}
}

func TestPushExporter_NilSenderAndNilExporterAreSafe(t *testing.T) {
	exporter := NewPushExporter(nil, PushExporterOptions{})
	if exporter != nil {
		t.Fatalf("NewPushExporter(nil) = %v, want nil", exporter)
	}

	// A typed-nil exporter must behave as the disabled trace feed, not panic - including
	// through the Exporter interface, where it is a non-nil interface value.
	exporter.Export(context.Background(), TraceEvent{Topic: "a"})
	exporter.Close()
	exporter.Close() // idempotent

	var iface Exporter = exporter
	iface.Export(context.Background(), TraceEvent{Topic: "b"})
}

func TestPushExporter_CloseIsIdempotent(t *testing.T) {
	sender := &captureSender{}
	exporter := NewPushExporter(sender, PushExporterOptions{})

	exporter.Export(context.Background(), TraceEvent{Topic: "a", Status: "Ok"})
	exporter.Close()
	exporter.Close()

	if got := sender.eventCount(); got != 1 {
		t.Errorf("eventCount() = %d, want 1", got)
	}
}

func TestPushExporter_DrainQueueTakesEverythingQueued(t *testing.T) {
	// Direct unit test of the shutdown drain: with events pre-queued and no competing
	// select cases, the path is deterministic (the loop-level races around Close are
	// covered by the lifecycle tests above).
	exporter := &PushExporter{queue: make(chan TraceEvent, 4)}
	exporter.queue <- TraceEvent{Topic: "a"}
	exporter.queue <- TraceEvent{Topic: "b"}

	batch := exporter.drainQueue([]TraceEvent{{Topic: "already-batched"}})

	if len(batch) != 3 || batch[0].Topic != "already-batched" || batch[1].Topic != "a" || batch[2].Topic != "b" {
		t.Errorf("drainQueue() = %v, want the prior batch plus both queued events in order", batch)
	}
}

func TestHeartbeat_WireFieldNamesAreCamelCase(t *testing.T) {
	data, err := json.Marshal(Heartbeat{Service: "orders", InstanceID: "orders-1", DescriptorHash: "sha256:x", SentAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{"service", "instanceId", "descriptorHash", "sentAt", "health"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("marshaled heartbeat is missing key %q: %s", key, data)
		}
	}
}
