package kafka

import (
	"context"
	"path/filepath"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"
)

// consumeOne runs a Consumer over builder until it has dispatched message.
func consumeOne(t *testing.T, builder *benzene.ApplicationBuilder, message kafkago.Message) {
	t.Helper()
	source := &fakeSource{messages: []kafkago.Message{message}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source.cancel = cancel
	if err := (&Consumer{Source: source, Builder: builder}).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// The vendored transport-metadata-cases.json (wire-contracts.md §2) run through the Consumer: the
// case's metadata travels as Kafka headers, and the reserved names are set once on the builder the
// Consumer inherits them from. The record carries no Kafka topic, so the fixture's unresolved cases
// stay unresolved rather than taking the Kafka-topic fallback, which
// TestConformance_TransportMetadata_TopicSource covers.
func TestConformance_TransportMetadata_Consumer(t *testing.T) {
	fixture := filepath.Join("..", "conformance", "testdata", transportmetadata.FixtureName)
	transportmetadata.Run(t, fixture, func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
		message := kafkago.Message{Value: []byte(transportmetadata.Body)}
		for name, value := range metadata {
			message.Headers = append(message.Headers, kafkago.Header{Key: name, Value: []byte(value)})
		}
		consumeOne(t, builder, message)
	})
}

// TestConformance_TransportMetadata_TopicSource is the kafka entry of topicSources: the topic comes
// from the `topic` header (the cases above), with the Kafka topic the record was consumed from as
// the fallback "so a producer that writes no topic header still routes" - and only as a fallback.
func TestConformance_TransportMetadata_TopicSource(t *testing.T) {
	t.Run("kafka-topic-fallback", func(t *testing.T) {
		builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
		consumeOne(t, builder, kafkago.Message{
			Topic:   transportmetadata.Topic,
			Headers: []kafkago.Header{{Key: "x-correlation-id", Value: []byte("abc-123")}},
			Value:   []byte(transportmetadata.Body),
		})
		if got := recorder.Only(t).Topic; got != transportmetadata.Topic {
			t.Errorf("topic = %q, want the Kafka topic %q when no topic header is present", got, transportmetadata.Topic)
		}
	})
	t.Run("topic-header-wins-over-the-kafka-topic", func(t *testing.T) {
		builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
		consumeOne(t, builder, kafkago.Message{
			Topic:   "benzene-events",
			Headers: []kafkago.Header{{Key: wire.DefaultTopicKey, Value: []byte(transportmetadata.Topic)}},
			Value:   []byte(transportmetadata.Body),
		})
		got := recorder.Only(t)
		if got.Topic != transportmetadata.Topic {
			t.Errorf("topic = %q, want the topic header %q", got.Topic, transportmetadata.Topic)
		}
		if _, ok := got.Headers[wire.DefaultTopicKey]; ok {
			t.Errorf("headers = %v, want the consumed topic header excluded", got.Headers)
		}
	})
}
