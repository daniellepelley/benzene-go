package rabbitmq

import (
	"context"
	"path/filepath"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
	"github.com/daniellepelley/benzene-go/wire"
	amqp "github.com/rabbitmq/amqp091-go"
)

var transportMetadataFixture = filepath.Join("..", "conformance", "testdata", transportmetadata.FixtureName)

// The vendored transport-metadata-cases.json (wire-contracts.md §2) run through the Consumer: the
// case's metadata travels as AMQP headers (BasicProperties.Headers, byte-array values as RabbitMQ
// carries them), and the reserved names are set once on the builder the Consumer inherits them
// from. The routing key is empty, so the fixture's unresolved cases stay unresolved rather than
// taking the routing-key fallback, which TestConformance_TransportMetadata_TopicSource covers.
func TestConformance_TransportMetadata_Consumer(t *testing.T) {
	transportmetadata.Run(t, transportMetadataFixture, func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
		headers := amqp.Table{}
		for name, value := range metadata {
			headers[name] = []byte(value)
		}
		consumer := &Consumer{Builder: builder}
		consumer.handle(context.Background(), delivery(&fakeAcknowledger{}, 1, headers, "", transportmetadata.Body))
	})
}

// TestConformance_TransportMetadata_TopicSource is the rabbitmq entry of topicSources: the topic
// comes from the metadata (the cases above), with the AMQP routing key as the fallback "so a
// non-Benzene producer still routes" - and only as a fallback.
func TestConformance_TransportMetadata_TopicSource(t *testing.T) {
	t.Run("routing-key-fallback", func(t *testing.T) {
		builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
		consumer := &Consumer{Builder: builder}
		consumer.handle(context.Background(), delivery(&fakeAcknowledger{}, 1, amqp.Table{"x-correlation-id": []byte("abc-123")}, transportmetadata.Topic, transportmetadata.Body))
		if got := recorder.Only(t).Topic; got != transportmetadata.Topic {
			t.Errorf("topic = %q, want the routing key %q when no topic header is present", got, transportmetadata.Topic)
		}
	})
	t.Run("topic-header-wins-over-the-routing-key", func(t *testing.T) {
		builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
		consumer := &Consumer{Builder: builder}
		consumer.handle(context.Background(), delivery(&fakeAcknowledger{}, 1, amqp.Table{"topic": []byte(transportmetadata.Topic)}, "some.routing.key", transportmetadata.Body))
		if got := recorder.Only(t).Topic; got != transportmetadata.Topic {
			t.Errorf("topic = %q, want the topic header %q", got, transportmetadata.Topic)
		}
	})
}
