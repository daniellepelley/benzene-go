package azureeventhub

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs/v2"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// ReceivedEvent is a transport-neutral view of one received Event Hubs event, the unit the
// Receiver hands the Consumer. It is deliberately this package's own small struct - not the SDK's
// *azeventhubs.ReceivedEventData - so the Consumer's receive-and-dispatch logic is exercisable
// against a fake with no SDK dependency in the test.
type ReceivedEvent struct {
	// Body is the raw event data bytes (azeventhubs.ReceivedEventData.Body).
	Body []byte
	// Properties are the event's application properties (azeventhubs.ReceivedEventData.Properties,
	// a map[string]any). The topic and headers are resolved from the string-valued entries.
	Properties map[string]any
	// SequenceNumber is the event's service-assigned sequence number
	// (azeventhubs.ReceivedEventData.SequenceNumber), carried for the OnFailure hook's diagnostics.
	SequenceNumber int64
	// Checkpoint is an opaque token the Receiver's adapter attaches so the Consumer's Checkpoint
	// hook can persist progress for this event (NewPartitionReceiver sets it to the underlying
	// *azeventhubs.ReceivedEventData). Nil when the Receiver does not support checkpointing.
	Checkpoint any
}

// Receiver is the narrow inbound interface the Consumer depends on: pull up to max events. A
// concrete Receiver (NewPartitionReceiver, over a real *azeventhubs.PartitionClient) owns how it
// connects, which partition it reads, and - because Event Hubs' durable checkpoint store is
// application-owned (see the package doc) - how it resumes. Depending on this interface keeps the
// Consumer testable with a fake, no live Azure needed.
type Receiver interface {
	ReceiveEvents(ctx context.Context, max int) ([]ReceivedEvent, error)
}

// Consumer runs a Benzene pipeline over an Event Hubs receiver. Each received event gets its own
// pipeline invocation - its own DI scope, via envelope.DispatchResult - and, on a successful
// dispatch, the optional Checkpoint hook is called so the caller can persist progress. It is the
// self-hosted counterpart of the outbound Client, the Go form of Benzene.Azure.EventHub's
// BenzeneEventHubWorker, but scoped to receive+dispatch only (the package doc explains why
// checkpointing is caller-provided rather than owned here).
type Consumer struct {
	Receiver Receiver
	Builder  *benzene.ApplicationBuilder

	// MaxBatchSize is how many events one ReceiveEvents call may return. Default 100 when <= 0.
	MaxBatchSize int

	// ErrorBackoff is how long Run waits after a receive or checkpoint error before trying again,
	// so a transient Event Hubs outage does not become a hot error loop. Default 1s when 0.
	ErrorBackoff time.Duration

	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2) used to read the
	// topic property. Leave it zero to inherit the Builder's reserved names (which default to
	// "topic"); set it only to a value that differs from the Builder's. Inheriting by default keeps
	// topic resolution matching the service's other bindings out of the box, exactly as awssqs's and
	// rabbitmq's consumers do - a service that renamed its topic key on the builder must not silently
	// misroute on the Event Hub consumer alone.
	ReservedNames wire.ReservedNames

	// OnFailure, when non-nil, is called for the FIRST event in a received batch whose dispatch
	// result is not a success status. Event Hubs delivers a partition's events in order and has no
	// per-event settlement, so - like the ordered-stream siblings awsdynamodb/awskinesis - the
	// Consumer STOPS at that first failure and does not checkpoint it or any later event in the
	// batch: the durable checkpoint never advances past an unhandled event, so on resume the failed
	// event redelivers (at-least-once) rather than being silently skipped. This deliberately diverges
	// from .NET's BenzeneEventHubConfig catch-and-continue default, matching this port's no-silent-
	// drop rule (the same reason awss3 returns a Go error instead of mirroring .NET's swallow).
	OnFailure func(ctx context.Context, event ReceivedEvent, resp wire.Response)

	// Checkpoint, when non-nil, is called after an event dispatches successfully so the caller can
	// persist durable progress (e.g. azeventhubs' ProcessorPartitionClient.UpdateCheckpoint using
	// event.Checkpoint). A returned error is treated like a transient receive error: Run backs off
	// and retries, so the events re-deliver (at-least-once) rather than being silently skipped.
	Checkpoint func(ctx context.Context, event ReceivedEvent) error

	// sleep is the backoff primitive, injectable for tests. nil means sleepContext.
	sleep func(ctx context.Context, d time.Duration)
}

// errMissingReceiver guards the two required fields with a clear message rather than a nil
// dereference deep in the loop.
var errMissingReceiver = errors.New("azureeventhub: Consumer requires both Receiver and Builder")

// Validate reports whether the Consumer is runnable - call it at startup for a clear error instead
// of a panic from Run's first receive.
func (c *Consumer) Validate() error {
	if c.Receiver == nil || c.Builder == nil {
		return errMissingReceiver
	}
	return nil
}

// Run receives and dispatches events until ctx is cancelled, then returns ctx.Err(). A receive or
// checkpoint error is not fatal: Run backs off (ErrorBackoff) and retries, so the loop survives a
// transient outage. Run one Consumer per goroutine; Event Hubs' at-least-once delivery means
// handlers must be idempotent.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.receiveAndDispatch(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.sleepFor(ctx, c.errorBackoff())
		}
	}
}

// receiveAndDispatch runs one receive/dispatch/checkpoint cycle. It is separated from Run so tests
// can drive a single deterministic cycle while Run wraps it in the lifecycle loop.
func (c *Consumer) receiveAndDispatch(ctx context.Context) error {
	events, err := c.Receiver.ReceiveEvents(ctx, c.maxBatchSize())
	if err != nil {
		return err
	}
	for _, event := range events {
		req := resolveEvent(event, c.topicKey())
		resp, successful := envelope.DispatchResult(ctx, c.Builder.Pipeline, c.Builder.Container, req)
		if !successful {
			// Ordered-stream stop-at-first-failure (like awsdynamodb/awskinesis): report the failure
			// and stop, WITHOUT checkpointing this event or processing any later one, so the durable
			// checkpoint never advances past unhandled work and the failed event redelivers on resume.
			if c.OnFailure != nil {
				c.OnFailure(ctx, event, resp)
			}
			return nil
		}
		if c.Checkpoint != nil {
			// Checkpoint on a cancellation-detached context so a graceful shutdown between a
			// successful dispatch and its checkpoint still persists the handled progress, rather than
			// cancelling the checkpoint and redelivering already-processed events - the same
			// settlement-outlives-cancellation choice awssqs.Consumer and azureservicebus.Worker make.
			if err := c.Checkpoint(context.WithoutCancel(ctx), event); err != nil {
				return err
			}
		}
	}
	return nil
}

// topicKey resolves the reserved topic property key: the Consumer's explicit override if set, else
// the Builder's reserved names (which default to "topic"). This mirrors awssqs's and rabbitmq's
// inherit-from-builder default so topic resolution matches the service's other bindings out of the
// box - a service that renamed its topic key on the builder must not silently misroute here.
func (c *Consumer) topicKey() string {
	if c.ReservedNames.TopicKey != "" {
		return c.ReservedNames.TopicKey
	}
	return c.Builder.ReservedNames.Topic()
}

func (c *Consumer) maxBatchSize() int {
	if c.MaxBatchSize <= 0 {
		return 100
	}
	return c.MaxBatchSize
}

func (c *Consumer) errorBackoff() time.Duration {
	if c.ErrorBackoff == 0 {
		return time.Second
	}
	return c.ErrorBackoff
}

func (c *Consumer) sleepFor(ctx context.Context, d time.Duration) {
	if c.sleep != nil {
		c.sleep(ctx, d)
		return
	}
	sleepContext(ctx, d)
}

// resolveEvent maps a received event onto a wire.Request exactly as the awssqs Consumer maps an SQS
// message: the topic from the reserved topic property (wire-contracts.md §2, the string-valued
// properties resolved by wire.ResolveMetadataTopic), the other string properties as headers, else -
// when no topic property is present - the body parsed as a full wire envelope. Non-string property
// values are ignored (Event Hubs properties are map[string]any; only strings carry Benzene topic
// and header text, matching the .NET headers/topic getters).
func resolveEvent(event ReceivedEvent, topicKey string) wire.Request {
	metadata := make(map[string]string, len(event.Properties))
	for name, value := range event.Properties {
		if s, ok := value.(string); ok {
			metadata[name] = s
		}
	}
	topic, headers := wire.ResolveMetadataTopic(metadata, topicKey)
	body := string(event.Body)

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: body}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal(event.Body, &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}
	return wire.Request{Topic: "", Headers: headers, Body: body}
}

// partitionReceiver bridges a real *azeventhubs.PartitionClient onto Receiver.
type partitionReceiver struct {
	client *azeventhubs.PartitionClient
}

// NewPartitionReceiver wraps a real *azeventhubs.PartitionClient so a Consumer can receive through
// it. The caller owns the client's connection, partition selection, and lifetime (Close it on
// shutdown), and owns durable checkpointing - each ReceivedEvent's Checkpoint token is the
// underlying *azeventhubs.ReceivedEventData, which a Checkpoint hook backed by an
// azeventhubs ProcessorPartitionClient can pass to UpdateCheckpoint. This is a thin pass-through to
// the real SDK; this package's tests exercise the Consumer against a Receiver fake instead, so the
// adapter itself is covered only by a live Event Hub.
func NewPartitionReceiver(client *azeventhubs.PartitionClient) Receiver {
	return partitionReceiver{client: client}
}

func (r partitionReceiver) ReceiveEvents(ctx context.Context, max int) ([]ReceivedEvent, error) {
	events, err := r.client.ReceiveEvents(ctx, max, nil)
	if err != nil {
		return nil, err
	}
	out := make([]ReceivedEvent, len(events))
	for i, e := range events {
		out[i] = ReceivedEvent{
			Body:           e.Body,
			Properties:     e.Properties,
			SequenceNumber: e.SequenceNumber,
			Checkpoint:     e,
		}
	}
	return out, nil
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
