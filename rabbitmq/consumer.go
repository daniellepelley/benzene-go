// Package rabbitmq is the self-hosted RabbitMQ binding, matching the main repo's Benzene.RabbitMq
// (RabbitMqWorker + the outbound publish client) / transport-bindings.md's self-hosted-worker
// shape: a Consumer loop that dispatches each broker delivery through a Benzene pipeline, and an
// outbound Client that publishes wire messages, applied to a self-hosted (or managed - CloudAMQP,
// Amazon MQ for RabbitMQ) broker.
//
// It is the direct sibling of the kafka module - another self-hosted broker binding in its own Go
// module, behind narrow interfaces (Publisher, DeliverySource) that the real amqp091 types satisfy
// as-is so this package's own tests run against fakes with no live broker. It depends on
// github.com/rabbitmq/amqp091-go (the maintained successor to streadway/amqp) - an AMQP 0-9-1
// broker wire protocol is not hand-rollable - which is why it lives in its own module: the root
// module stays zero-dependency.
//
// Mapping (matching Benzene.RabbitMq's RabbitMqMessageTopicGetter / RabbitMqContextConverter):
//
//   - Topic: read from the reserved "topic" message header (wire-contracts.md §2 tier A, the
//     portable choice every Benzene queue transport shares), falling back to the AMQP routing key
//     when that header is absent (so a non-Benzene producer that routes on its natural routing key
//     still resolves), falling back finally to a whole envelope parsed out of the body (this port's
//     convention, matching awssqs). The outbound Client writes both - routing key AND header - so a
//     Benzene-to-Benzene message round-trips on the header regardless of the exchange binding.
//   - Headers: every message header, verbatim (RabbitMQ carries them as []byte on the wire, which
//     this decodes to strings; a raw string value is accepted too), minus the topic header, which
//     is lifted out as the topic.
//   - Body: the raw message body, verbatim, both directions - no envelope wrapping on publish.
//
// Failure semantics: unlike Kafka (no broker-side redelivery), RabbitMQ HAS broker redelivery, so
// this Consumer settles each delivery from its handler's outcome the way Benzene.RabbitMq's
// Explicit ack mode does - BasicAck on a successful dispatch, BasicNack on an unsuccessful one. A
// nacked delivery is requeued for another attempt by default, but the requeue is BOUNDED to one
// retry (a delivery whose Redelivered flag is already set is nacked WITHOUT requeue) so a poison
// message cannot hot-loop the queue forever. For a higher, precise redelivery limit, set NoRequeue
// and configure a dead-letter exchange on the broker - the production setting. The OnFailure hook
// fires before the nack, the log/dead-letter extension point, playing the reference
// implementation's ILogger role explicitly. Settlement errors (ack/nack failing because the channel
// or connection dropped) are best-effort and not surfaced - the delivery's stream will close next
// and Run returns, so a restarted Consumer resumes with at-least-once delivery.
package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// DeliverySource is the narrow slice of *amqp091.Channel the Consumer depends on: open a stream of
// deliveries for a queue. Depending on this interface (not the concrete *amqp091.Channel) keeps the
// Consumer testable with a fake, no live broker needed; an *amqp091.Channel satisfies it as-is (its
// Consume returns exactly this channel). The Consumer consumes with autoAck=false, so each delivery
// is settled explicitly from its handler's outcome - see the package doc.
type DeliverySource interface {
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
}

// Consumer runs a Benzene pipeline over a RabbitMQ queue. Each delivery gets its own pipeline
// invocation - its own DI scope, via envelope.DispatchResult - and is acked on success or nacked on
// failure (see the package doc for the requeue/poison-message contract). It is the self-hosted
// counterpart of the outbound Client, and the RabbitMQ sibling of kafka.Consumer.
type Consumer struct {
	// Source opens the delivery stream (typically an *amqp091.Channel).
	Source DeliverySource
	// Builder supplies the pipeline and container each delivery is dispatched through.
	Builder *benzene.ApplicationBuilder

	// Queue is the name of the queue to consume.
	Queue string
	// ConsumerTag names this consumer to the broker; empty lets the broker generate one.
	ConsumerTag string

	// NoRequeue, when true, nacks a failed delivery WITHOUT requeue, so it goes to the queue's
	// dead-letter exchange (if configured) or is dropped - the setting to use when broker-side
	// dead-lettering owns the retry policy. The zero value (false) is the safe default matching
	// Benzene.RabbitMq's RequeueOnFailure=true: a first-attempt failure is requeued, and only an
	// already-redelivered failure is dropped/dead-lettered (the one-retry poison bound).
	NoRequeue bool

	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2) used to read the
	// topic header. An empty TopicKey inherits the Builder's reserved names (so a service that
	// renamed the key on its builder gets the same key here without extra wiring); set TopicKey to
	// override explicitly. Use the SAME key the publisher wrote.
	ReservedNames wire.ReservedNames

	// OnFailure, when non-nil, is called for each delivery whose dispatch result is not a success
	// status, before that delivery is nacked. This is where a failure log or metric belongs. A nil
	// OnFailure means unsuccessful deliveries are simply nacked (requeued/dead-lettered per
	// NoRequeue) with no notification.
	OnFailure func(ctx context.Context, delivery amqp.Delivery, resp wire.Response)
}

// errMissingBuilder guards the two required fields with a clear message rather than a nil
// dereference deep inside Run.
var errMissingBuilder = errors.New("rabbitmq: Consumer requires both Source and Builder")

// errDeliveriesClosed is returned when the delivery stream closes while the context is still live -
// a broker/connection drop, actionable by the owner (reconnect), as distinct from a clean
// context-cancelled shutdown (which returns nil).
var errDeliveriesClosed = errors.New("rabbitmq: delivery stream closed by the broker")

// Validate reports whether the Consumer is runnable - call it at startup for a clear error instead
// of a panic from Run.
func (c *Consumer) Validate() error {
	if c.Source == nil || c.Builder == nil {
		return errMissingBuilder
	}
	return nil
}

// Run opens the delivery stream and dispatches deliveries until ctx is cancelled (returns nil - the
// clean shutdown) or the stream closes underneath it (returns errDeliveriesClosed, deliveries in
// flight left unsettled so the broker redelivers them - at-least-once). A delivery that fails to
// dispatch never stops the loop; it is nacked and the loop continues (see the package doc). Opening
// the stream is the one fatal step: a Consume error returns immediately, wrapped.
func (c *Consumer) Run(ctx context.Context) error {
	deliveries, err := c.Source.Consume(c.Queue, c.ConsumerTag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("rabbitmq: consume failed: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errDeliveriesClosed
			}
			c.handle(ctx, delivery)
		}
	}
}

// handle dispatches one delivery in its own DI scope and settles it: ack on a successful result,
// otherwise fire OnFailure and nack (requeued unless NoRequeue or the delivery was already
// redelivered). Settlement errors are best-effort - see the package doc.
func (c *Consumer) handle(ctx context.Context, delivery amqp.Delivery) {
	resp, successful := envelope.DispatchResult(ctx, c.Builder.Pipeline, c.Builder.Container, c.resolveRequest(delivery))
	if successful {
		_ = delivery.Ack(false)
		return
	}
	if c.OnFailure != nil {
		c.OnFailure(ctx, delivery, resp)
	}
	// Requeue by default, but only once: an already-redelivered failure is dropped/dead-lettered so
	// a poison message can't hot-loop (matching Benzene.RabbitMq's bounded requeue).
	requeue := !c.NoRequeue && !delivery.Redelivered
	_ = delivery.Nack(false, requeue)
}

// topicKey resolves the reserved topic header key: the Consumer's explicit override if set, else
// the Builder's reserved names (which default to "topic"). This mirrors awssqs's inherit-from-
// builder default so topic resolution matches the service's other bindings out of the box.
func (c *Consumer) topicKey() string {
	if c.ReservedNames.TopicKey != "" {
		return c.ReservedNames.TopicKey
	}
	return c.Builder.ReservedNames.Topic()
}

// resolveRequest maps a delivery onto a wire.Request per the package doc: topic from the reserved
// header, else the AMQP routing key, else a whole envelope parsed out of the body; the other
// headers pass through verbatim; the body is verbatim.
func (c *Consumer) resolveRequest(delivery amqp.Delivery) wire.Request {
	metadata := make(map[string]string, len(delivery.Headers))
	for name, value := range delivery.Headers {
		metadata[name] = headerString(value)
	}
	topic, headers := wire.ResolveMetadataTopic(metadata, c.topicKey())
	body := string(delivery.Body)

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: body}
	}

	// Routing-key fallback: a non-Benzene producer that didn't set the topic header still routes by
	// its natural AMQP routing key.
	if delivery.RoutingKey != "" {
		return wire.Request{Topic: delivery.RoutingKey, Headers: headers, Body: body}
	}

	// Envelope-in-body fallback (this port's convention, like awssqs): the whole wire envelope
	// travels in the body when the transport carried no routing metadata at all.
	var envelopeReq wire.Request
	if err := json.Unmarshal(delivery.Body, &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}
	return wire.Request{Topic: "", Headers: headers, Body: body}
}

// headerString decodes an AMQP header value to a string: RabbitMQ carries header values as []byte
// on the wire, but a client may set a raw string, so accept both (and stringify anything else).
// A nil value becomes the empty string.
func headerString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}
