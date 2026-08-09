package azureservicebus

import (
	"context"
	"encoding/json"
	"errors"
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
	// API is the Service Bus receiver (typically an *azservicebus.Receiver). Required.
	API ReceiverAPI
	// Builder is the application whose pipeline each message dispatches through. Required.
	Builder *benzene.ApplicationBuilder

	// MaxMessages is the most messages one ReceiveMessages call may return. The SDK caps the wait
	// internally, so ReceiveMessages returns a partial batch (or none) rather than blocking for the
	// full count. Default 10 when <= 0.
	MaxMessages int
	// ErrorBackoff is how long Run waits after a ReceiveMessages error before receiving again, so a
	// transient Service Bus outage doesn't become a hot error loop. Default 1s when 0.
	ErrorBackoff time.Duration
	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2) used to read the
	// topic application property. Leave it zero to inherit the Builder's reserved names (so topic
	// resolution matches the outbound Client out of the box); set it only to differ from the Builder.
	ReservedNames wire.ReservedNames
	// AckMode selects how a message whose dispatch failed is settled (AckModeAbandon, the default
	// zero value, or AckModeDeadLetter). A successful dispatch always completes regardless.
	AckMode AckMode
	// DeadLetterReason and DeadLetterDescription are recorded when a failed message is dead-lettered
	// under AckModeDeadLetter. Both are short diagnostic codes, never secrets; empty ones are omitted.
	// They have no effect under AckModeAbandon.
	DeadLetterReason      string
	DeadLetterDescription string
	// OnFailure, when non-nil, is called for each message whose dispatch was unsuccessful (the
	// message is abandoned or dead-lettered per AckMode regardless). Use it to log or emit a metric.
	OnFailure func(messageID string, response wire.Response)

	// sleep is the backoff primitive, injectable for tests. nil means sleepContext.
	sleep func(ctx context.Context, d time.Duration)
}

// errMissingDeps guards the required fields with a clear message rather than a nil dereference deep
// in the receive loop.
var errMissingDeps = errors.New("azureservicebus: Worker requires both API and Builder")

// Validate reports whether the Worker is runnable - call it at startup for a clear error instead of
// a panic from Run's first receive.
func (w *Worker) Validate() error {
	if w.API == nil || w.Builder == nil {
		return errMissingDeps
	}
	return nil
}

func (w *Worker) maxMessages() int {
	if w.MaxMessages <= 0 {
		return 10
	}
	return w.MaxMessages
}

func (w *Worker) errorBackoff() time.Duration {
	if w.ErrorBackoff == 0 {
		return time.Second
	}
	return w.ErrorBackoff
}

// topicKey resolves the reserved topic application-property key: the Worker's explicit override if
// set, else the Builder's reserved names (which default to "topic"), so topic resolution matches the
// outbound Client out of the box.
func (w *Worker) topicKey() string {
	if w.ReservedNames.TopicKey != "" {
		return w.ReservedNames.TopicKey
	}
	return w.Builder.ReservedNames.Topic()
}

func (w *Worker) sleepFor(ctx context.Context, d time.Duration) {
	if w.sleep != nil {
		w.sleep(ctx, d)
		return
	}
	sleepContext(ctx, d)
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
			w.sleepFor(ctx, w.errorBackoff())
		}
	}
}

// poll runs one receive/dispatch/settle cycle. It is a single deterministic cycle for tests, while
// Run wraps it in the lifecycle loop.
func (w *Worker) poll(ctx context.Context) error {
	messages, err := w.API.ReceiveMessages(ctx, w.maxMessages(), nil)
	if err != nil {
		return err
	}
	for _, message := range messages {
		req := resolveMessage(message, w.topicKey())
		response, successful := envelope.DispatchResult(ctx, w.Builder.Pipeline, w.Builder.Container, req)
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
		if err := w.API.CompleteMessage(settleCtx, message, nil); err != nil {
			w.reportFailure(message, wire.Response{StatusCode: string(benzene.StatusServiceUnavailable)})
		}
		return
	}

	w.reportFailure(message, response)

	if w.AckMode == AckModeDeadLetter {
		_ = w.API.DeadLetterMessage(settleCtx, message, &azservicebus.DeadLetterOptions{
			Reason:           optionalString(w.DeadLetterReason),
			ErrorDescription: optionalString(w.DeadLetterDescription),
		})
		return
	}
	_ = w.API.AbandonMessage(settleCtx, message, nil)
}

// optionalString returns a pointer to s, or nil when s is empty, so an unset DeadLetterReason /
// DeadLetterDescription is omitted from the SDK options rather than sent as an empty string.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (w *Worker) reportFailure(message *azservicebus.ReceivedMessage, response wire.Response) {
	if w.OnFailure != nil {
		w.OnFailure(message.MessageID, response)
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
