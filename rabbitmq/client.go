package rabbitmq

import (
	"context"
	"encoding/json"

	amqp "github.com/rabbitmq/amqp091-go"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// Publisher is the narrow slice of *amqp091.Channel the Client depends on: publish one message
// to an exchange with a routing key. Depending on this interface, rather than the concrete
// *amqp091.Channel, keeps the Client testable with a fake - no live broker needed. An
// *amqp091.Channel satisfies it as-is (its PublishWithContext has exactly this signature). The
// method mirrors amqp091's own so the boolean order (mandatory, immediate) is the one a reader
// already knows from the SDK.
type Publisher interface {
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
}

// Client publishes outbound Benzene messages to RabbitMQ. It satisfies client.Sender, so it can
// be wrapped in client.CorrelationDecorator/RetryDecorator like any other Sender, and mirrors the
// main repo's Benzene.RabbitMq outbound client (RabbitMqBenzeneMessageClient /
// RabbitMqContextConverter): the Benzene topic becomes the AMQP routing key and is ALSO carried as
// a reserved "topic" message header, so a Benzene RabbitMQ Consumer routes by header (portable)
// with the routing key as the idiomatic fallback. The Benzene header dictionary is forwarded onto
// the message's headers table (as []byte values, RabbitMQ's wire form) so correlation/trace/version
// decorators reach the wire exactly as on the other transports.
type Client struct {
	// Publisher is the channel this client publishes on (typically an *amqp091.Channel).
	Publisher Publisher

	// Exchange is the exchange to publish to. The empty string is the default exchange, where the
	// routing key is the target queue name - so a Client with an empty Exchange publishes directly
	// to the queue named after the topic.
	Exchange string

	// Mandatory, when true, asks the broker to return an unroutable message (no queue bound for the
	// routing key) rather than silently drop it. The return itself is delivered asynchronously on
	// the channel's return listener, which this client does not observe - a mandatory publish that
	// is later returned still reports StatusAccepted here. Default false.
	Mandatory bool

	// Persistent controls the published delivery mode. NewClient defaults it to true (delivery mode
	// 2 - persistent), so a message on a durable queue survives a broker restart, matching the .NET
	// client's default. A zero-value Client (Persistent false) publishes transiently (delivery mode
	// 1); construct via NewClient for the durable default.
	Persistent bool

	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2) - here, the header
	// key the topic is written to. Empty uses the "topic" default. Set it to the SAME value the
	// consuming service reads, per the spec's "override applies to both directions" rule.
	ReservedNames wire.ReservedNames
}

// NewClient returns a Client publishing to exchange via publisher (typically an *amqp091.Channel),
// with persistent delivery (delivery mode 2) - the durable default. Pass the empty string for
// exchange to publish to the default exchange (routing key = queue name).
func NewClient(publisher Publisher, exchange string) *Client {
	return &Client{Publisher: publisher, Exchange: exchange, Persistent: true}
}

// Send publishes message to c.Exchange with topic as the AMQP routing key, carrying topic also as
// the reserved "topic" header and every caller header on the message's headers table (as []byte).
// A successful publish maps to StatusAccepted ("accepted for asynchronous processing"); a
// transport-level failure maps to ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	table := make(amqp.Table, len(headers)+1)
	for k, v := range headers {
		table[k] = []byte(v)
	}
	// Carry the topic as a header too, so a header-first Consumer round-trips it regardless of the
	// routing key the exchange binding uses (matching .NET's RabbitMqContextConverter).
	table[c.ReservedNames.Topic()] = []byte(topic.String())

	deliveryMode := amqp.Transient
	if c.Persistent {
		deliveryMode = amqp.Persistent
	}

	msg := amqp.Publishing{
		Headers:      table,
		ContentType:  "application/json",
		DeliveryMode: deliveryMode,
		Body:         message,
	}

	// immediate is always false: it was removed from the AMQP 0-9-1 spec and RabbitMQ rejects it.
	if err := c.Publisher.PublishWithContext(ctx, c.Exchange, topic.String(), c.Mandatory, false, msg); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("rabbitmq: publish failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
