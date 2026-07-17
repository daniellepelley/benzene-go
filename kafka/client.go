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
// live broker needed. A *kafka.Writer satisfies it as-is, but MUST be constructed with its
// own Topic field left empty: Client sets Topic per message (see Send), and kafka-go's
// *Writer.WriteMessages rejects a message that sets Topic when the Writer already has one
// configured ("Topic must not be specified for both Writer and Message").
type MessageWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// Client publishes outbound Benzene messages to Kafka. It satisfies client.Sender, so it can
// be wrapped in client.CorrelationDecorator/RetryDecorator like any other Sender.
type Client struct {
	Writer MessageWriter

	// Key, when non-nil, computes the Kafka message key for a Send call - Kafka assigns a
	// keyed message's partition by hashing the key, so records sharing a key stay ordered.
	// Leave nil for the writer's default balancer.
	Key func(topic benzene.Topic, message []byte) []byte
}

// NewClient returns a Client publishing via writer (typically a *kafka.Writer constructed
// with your brokers, and no fixed Topic - see MessageWriter).
func NewClient(writer MessageWriter) *Client {
	return &Client{Writer: writer}
}

// Send publishes message to the Kafka topic named after topic (per the package doc, one
// Kafka topic per Benzene topic - matching the main repo's
// Benzene.Kafka.Core.Kafka.KafkaClientMiddleware, which produces to
// context.Topic == the Benzene request's Topic verbatim), with headers written as Kafka
// message headers verbatim - no reserved header name, no envelope wrapping. A successful
// publish maps to StatusAccepted ("accepted for asynchronous processing"); a transport-level
// failure maps to ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	kafkaHeaders := make([]kafkago.Header, 0, len(headers))
	for k, v := range headers {
		kafkaHeaders = append(kafkaHeaders, kafkago.Header{Key: k, Value: []byte(v)})
	}

	msg := kafkago.Message{Topic: topic.String(), Value: message, Headers: kafkaHeaders}
	if c.Key != nil {
		msg.Key = c.Key(topic, message)
	}

	if err := c.Writer.WriteMessages(ctx, msg); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("kafka: write failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
