package azureservicebus

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// ReceiverAPI is the narrow slice of the Service Bus SDK the Worker depends on - receive a batch of
// messages, then settle each one (complete/abandon/dead-letter). Depending on this interface (not the
// concrete *azservicebus.Receiver) keeps the Worker testable with a fake, no live Service Bus needed;
// *azservicebus.Receiver satisfies it as-is. It is the pull-loop counterpart of the .NET
// BenzeneServiceBusWorker's push ServiceBusProcessor: the same topic/body/header resolution and the
// same success->complete / failure->abandon(or dead-letter) settlement, but this owns the receive
// loop rather than being pushed each message by a processor - matching this port's awssqs.Consumer.
type ReceiverAPI interface {
	ReceiveMessages(ctx context.Context, maxMessages int, options *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error)
	CompleteMessage(ctx context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.CompleteMessageOptions) error
	AbandonMessage(ctx context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.AbandonMessageOptions) error
	DeadLetterMessage(ctx context.Context, message *azservicebus.ReceivedMessage, options *azservicebus.DeadLetterOptions) error
}

// AckMode selects how the Worker settles a message whose dispatch was unsuccessful. A successful
// dispatch always completes the message; this only controls the failure settlement, mirroring the
// choice the .NET worker's ServiceBusSettlement enum exposes (Complete on success, then Abandon or
// DeadLetter on failure).
type AckMode int

const (
	// AckModeAbandon (the default) abandons a message whose dispatch failed, releasing its lock so
	// Service Bus redelivers it (subject to the entity's own max-delivery-count, after which the
	// entity's dead-letter policy moves it aside). This is the safe default - Benzene never
	// quarantines a message itself, matching the .NET worker's success->complete/failure->abandon
	// default. Handlers must be idempotent (at-least-once).
	AckModeAbandon AckMode = iota

	// AckModeDeadLetter dead-letters a message whose dispatch failed immediately, moving it to the
	// entity's dead-letter sub-queue (with the reason/description from WithDeadLetter) instead of
	// abandon-looping to the max-delivery-count. Use it when a failed message is poison and should be
	// quarantined for inspection rather than retried.
	AckModeDeadLetter
)

// Worker is a self-hosted Service Bus consumer (the .NET BenzeneServiceBusWorker): it receives from a
// queue or subscription and runs each received message through the pipeline in its own DI scope,
// completing the messages whose dispatch succeeded and abandoning (or dead-lettering, per AckMode) the
// ones whose dispatch was unsuccessful. An abandoned message reappears for redelivery, so this Worker
// never completes an unhandled message. It is the standalone alternative to the Azure Functions
// Service Bus trigger (azurefunctions.QueueHandler) for a service that owns its own compute.
type Worker struct {
	api     ReceiverAPI
	builder *benzene.ApplicationBuilder

	maxMessages           int
	errorBackoff          time.Duration
	reservedNames         wire.ReservedNames
	ackMode               AckMode
	deadLetterReason      *string
	deadLetterDescription *string
	onFailure             func(messageID string, response wire.Response)
	sleep                 func(ctx context.Context, d time.Duration)
}

// WorkerOption configures a Worker.
type WorkerOption func(*Worker)

// WithMaxMessages sets the most messages one ReceiveMessages call may return. The SDK caps the wait
// internally, so ReceiveMessages returns a partial batch (or none) rather than blocking for the full
// count. Default 10.
func WithMaxMessages(n int) WorkerOption { return func(w *Worker) { w.maxMessages = n } }

// WithErrorBackoff sets how long Run waits after a ReceiveMessages error before receiving again, so a
// transient Service Bus outage doesn't become a hot error loop. Default 1s.
func WithErrorBackoff(d time.Duration) WorkerOption {
	return func(w *Worker) { w.errorBackoff = d }
}

// WithWorkerReservedNames overrides the reserved metadata names (wire-contracts.md §2) used to read
// the topic application property. Set it to the SAME value the publisher wrote.
func WithWorkerReservedNames(names wire.ReservedNames) WorkerOption {
	return func(w *Worker) { w.reservedNames = names }
}

// WithAckMode selects how a message whose dispatch failed is settled (AckModeAbandon, the default, or
// AckModeDeadLetter). A successful dispatch always completes regardless.
func WithAckMode(mode AckMode) WorkerOption {
	return func(w *Worker) { w.ackMode = mode }
}

// WithDeadLetter sets the reason and description recorded when a failed message is dead-lettered
// under AckModeDeadLetter. Both are short diagnostic codes, never secrets. It has no effect under
// AckModeAbandon.
func WithDeadLetter(reason, description string) WorkerOption {
	return func(w *Worker) {
		w.deadLetterReason = &reason
		w.deadLetterDescription = &description
	}
}

// WithOnFailure registers a hook called for each message whose dispatch was unsuccessful (the message
// is abandoned or dead-lettered per AckMode regardless). Use it to log or emit a metric.
func WithOnFailure(hook func(messageID string, response wire.Response)) WorkerOption {
	return func(w *Worker) { w.onFailure = hook }
}

// NewWorker builds a Worker receiving via api (typically an *azservicebus.Receiver) and dispatching
// through builder's pipeline.
func NewWorker(api ReceiverAPI, builder *benzene.ApplicationBuilder, opts ...WorkerOption) *Worker {
	w := &Worker{
		api:     api,
		builder: builder,
		// Default to the builder's reserved names (wire-contracts.md §2 / app.go's UseReservedNames)
		// so topic resolution matches the outbound Client out of the box; a service that overrode the
		// topic key on its builder gets the same key here without extra wiring.
		reservedNames: builder.ReservedNames,
		maxMessages:   10,
		errorBackoff:  time.Second,
		sleep:         sleepContext,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Run receives and dispatches until ctx is cancelled, then returns ctx.Err(). A receive error is not
// fatal: Run backs off (WithErrorBackoff) and keeps receiving, so the loop survives a transient
// Service Bus outage. Run one Worker per goroutine; Service Bus's at-least-once delivery means
// handlers must be idempotent.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.poll(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.sleep(ctx, w.errorBackoff)
		}
	}
}

// poll runs one receive/dispatch/settle cycle. It is a single deterministic cycle for tests, while
// Run wraps it in the lifecycle loop.
func (w *Worker) poll(ctx context.Context) error {
	messages, err := w.api.ReceiveMessages(ctx, w.maxMessages, nil)
	if err != nil {
		return err
	}
	for _, message := range messages {
		req := resolveMessage(message, w.reservedNames.Topic())
		response, successful := envelope.DispatchResult(ctx, w.builder.Pipeline, w.builder.Container, req)
		w.settle(ctx, message, response, successful)
	}
	return nil
}

// settle completes a successfully-dispatched message, or abandons/dead-letters an unsuccessful one per
// AckMode. Settlement runs on a cancellation-detached context so a graceful shutdown (ctx cancelled
// after a message was handled) still acks the completed work, rather than cancelling the settle and
// letting Service Bus redeliver already-processed messages - the same settlement-outlives-cancellation
// choice awssqs.Consumer and the idempotency package make. Abandon/dead-letter settle errors are
// swallowed: the message's lock simply expires and it redelivers, which is safe under at-least-once.
func (w *Worker) settle(ctx context.Context, message *azservicebus.ReceivedMessage, response wire.Response, successful bool) {
	settleCtx := context.WithoutCancel(ctx)

	if successful {
		// A failed complete of already-handled work means Service Bus will redeliver it after the lock
		// expires - safe (at-least-once), never a drop, but surface it via the hook so the redelivery
		// is observable rather than silent, matching awssqs's partial-delete-failure reporting.
		if err := w.api.CompleteMessage(settleCtx, message, nil); err != nil {
			w.reportFailure(message, wire.Response{StatusCode: string(benzene.StatusServiceUnavailable)})
		}
		return
	}

	w.reportFailure(message, response)

	if w.ackMode == AckModeDeadLetter {
		_ = w.api.DeadLetterMessage(settleCtx, message, &azservicebus.DeadLetterOptions{
			Reason:           w.deadLetterReason,
			ErrorDescription: w.deadLetterDescription,
		})
		return
	}
	_ = w.api.AbandonMessage(settleCtx, message, nil)
}

func (w *Worker) reportFailure(message *azservicebus.ReceivedMessage, response wire.Response) {
	if w.onFailure != nil {
		w.onFailure(message.MessageID, response)
	}
}

// resolveMessage resolves a Service Bus received message into a wire.Request the same way awssqs's
// self-hosted Consumer resolves an SQS message: the topic from the reserved topic application property
// (wire-contracts.md §2), the other string-typed application properties as headers, else the body
// parsed as a full envelope. Non-string application properties are ignored, matching the .NET
// ServiceBusConsumerMessageHeadersGetter's "every string-typed application property is a header".
func resolveMessage(message *azservicebus.ReceivedMessage, topicKey string) wire.Request {
	metadata := make(map[string]string, len(message.ApplicationProperties))
	for name, value := range message.ApplicationProperties {
		if s, ok := value.(string); ok {
			metadata[name] = s
		}
	}
	topic, headers := wire.ResolveMetadataTopic(metadata, topicKey)
	body := string(message.Body)

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: body}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal([]byte(body), &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}
	return wire.Request{Topic: "", Headers: headers, Body: body}
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
