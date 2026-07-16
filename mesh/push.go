package mesh

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// Sender is the outbound-client subset PushExporter needs - transport-bindings.md §2's
// one send interface, satisfied by *httpclient.Client. An interface rather than the
// concrete client so the trace feed isn't married to HTTP: any transport that can carry a
// wire envelope can carry mesh traffic.
type Sender interface {
	Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage]
}

// PushExporterOptions tunes a PushExporter. Zero values mean the defaults.
type PushExporterOptions struct {
	// BatchSize is the number of events that triggers an immediate flush. Default 64.
	BatchSize int
	// FlushInterval is how often a partial batch is flushed anyway, so a quiet service's
	// traces still arrive promptly. Default 5s.
	FlushInterval time.Duration
	// BufferSize is the queue between invocations and the background sender. When it is
	// full, Export drops new events rather than blocking. Default 1024.
	BufferSize int
}

// PushExporter batches TraceEvents and sends them to a collector as mesh:traces
// envelopes (mesh.md §8 Phase 3) from a single background goroutine, so exporting never
// runs on an invocation's goroutine beyond a non-blocking channel send.
//
// The trace feed is lossy by design, in every failure mode: a full buffer drops the new
// event, a failed send drops the batch (the Sender contract already converts transport
// failures to a Result, never an error), and a nil Sender yields a nil exporter whose
// methods are all nil-safe no-ops. The mesh must never make a service slower or less
// reliable than an unmeshed one.
type PushExporter struct {
	sender    Sender
	batchSize int
	interval  time.Duration
	queue     chan TraceEvent
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewPushExporter starts the background sender and returns the exporter. A nil sender
// returns a nil exporter - usable directly as TraceMiddleware's exporter thanks to
// nil-safe methods, degrading to a disabled trace feed.
func NewPushExporter(sender Sender, options PushExporterOptions) *PushExporter {
	if sender == nil {
		return nil
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 64
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = 5 * time.Second
	}
	if options.BufferSize <= 0 {
		options.BufferSize = 1024
	}

	exporter := &PushExporter{
		sender:    sender,
		batchSize: options.BatchSize,
		interval:  options.FlushInterval,
		queue:     make(chan TraceEvent, options.BufferSize),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go exporter.loop()
	return exporter
}

// Export enqueues event for the background sender. Never blocks: a full buffer drops the
// event. Nil-safe, so a (*PushExporter)(nil) wired in as an Exporter behaves as the
// disabled trace feed rather than panicking.
func (e *PushExporter) Export(_ context.Context, event TraceEvent) {
	if e == nil {
		return
	}
	select {
	case e.queue <- event:
	default: // buffer full: drop, lossy by design
	}
}

// Close flushes everything already queued and stops the background sender. Safe to call
// more than once and on a nil exporter. Call it on shutdown so the tail of the trace feed
// isn't lost with the process.
func (e *PushExporter) Close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() { close(e.stop) })
	<-e.done
}

func (e *PushExporter) loop() {
	defer close(e.done)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	batch := make([]TraceEvent, 0, e.batchSize)
	for {
		select {
		case event := <-e.queue:
			batch = append(batch, event)
			if len(batch) >= e.batchSize {
				batch = e.flush(batch)
			}
		case <-ticker.C:
			batch = e.flush(batch)
		case <-e.stop:
			e.flush(e.drainQueue(batch)) // one final batch, whatever its size
			return
		}
	}
}

// drainQueue appends everything already queued to batch - the shutdown path, so the tail
// of the feed isn't lost with the process.
func (e *PushExporter) drainQueue(batch []TraceEvent) []TraceEvent {
	for {
		select {
		case event := <-e.queue:
			batch = append(batch, event)
		default:
			return batch
		}
	}
}

// flush sends batch as one mesh:traces envelope and returns the emptied slice. The send
// result is deliberately ignored: a rejected or undeliverable batch is dropped, not
// retried - lossy by design.
func (e *PushExporter) flush(batch []TraceEvent) []TraceEvent {
	if len(batch) == 0 {
		return batch
	}
	// TraceBatch has the same always-marshalable field types as TraceEvent, so Marshal
	// cannot fail.
	data, _ := json.Marshal(TraceBatch{Events: batch})
	e.sender.Send(context.Background(), benzene.NewTopic(TopicTraces), map[string]string{}, data)
	return batch[:0]
}
