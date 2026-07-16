package kafka

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	kafkago "github.com/segmentio/kafka-go"
)

// MessageWriter is the single kafka-go method Client depends on. Depending on this narrow
// interface, rather than the concrete *kafka.Writer, makes Client testable with a fake - no
// live broker needed. A *kafka.Writer (configured with its target Kafka topic) satisfies it
// as-is.
type MessageWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// Client publishes outbound Benzene messages to a Kafka topic (the stream is the writer's
// configuration; the Benzene topic travels as a message header - see the package doc). It
// satisfies client.Sender, so it can be wrapped in client.CorrelationDecorator/RetryDecorator
// like any other Sender.
type Client struct {
	Writer MessageWriter

	// Key, when non-nil, computes the Kafka message key for a Send call - Kafka assigns a
	// keyed message's partition by hashing the key, so records sharing a key stay ordered.
	// Leave nil for the writer's default balancer.
	Key func(topic benzene.Topic, message []byte) []byte
}

// NewClient returns a Client publishing via writer (typically a *kafka.Writer constructed
// with your brokers and target Kafka topic).
func NewClient(writer MessageWriter) *Client {
	return &Client{Writer: writer}
}

// Send publishes message, with topic written as a "topic" message header per
// wire-contracts.md §2 and headers written as additional message headers. A successful
// publish maps to StatusAccepted ("accepted for asynchronous processing"); a transport-level
// failure maps to ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	kafkaHeaders := make([]kafkago.Header, 0, len(headers)+1)
	kafkaHeaders = append(kafkaHeaders, kafkago.Header{Key: "topic", Value: []byte(topic.String())})
	for k, v := range headers {
		kafkaHeaders = append(kafkaHeaders, kafkago.Header{Key: k, Value: []byte(v)})
	}

	msg := kafkago.Message{Value: message, Headers: kafkaHeaders}
	if c.Key != nil {
		msg.Key = c.Key(topic, message)
	}

	if err := c.Writer.WriteMessages(ctx, msg); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("kafka: write failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
