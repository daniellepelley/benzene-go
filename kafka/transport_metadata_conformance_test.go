package kafka

import (
	"context"
	"testing"

	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"
)

// TestConformance_TransportMetadata_TopicSource is the kafka entry of transport-metadata-cases.json's
// topicSources, and it records a divergence rather than a pass. The fixture (with wire-contracts §2)
// says a Kafka consumer reads the topic from the `topic` header; this Consumer - like .NET's
// KafkaMessageTopicGetter and TypeScript's - routes on the Kafka topic itself, and this module's
// Client writes no `topic` header (Python follows the fixture). The root conformance runner lists it
// in topicSourceDivergences as a question for the spec; this test holds the claim to what the
// Consumer does, so it cannot drift silently while the question is open.
func TestConformance_TransportMetadata_TopicSource(t *testing.T) {
	builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
	source := &fakeSource{messages: []kafkago.Message{{
		Topic:   transportmetadata.Topic,
		Headers: []kafkago.Header{{Key: wire.DefaultTopicKey, Value: []byte("not-the-topic")}},
		Value:   []byte(transportmetadata.Body),
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source.cancel = cancel
	if err := (&Consumer{Source: source, Builder: builder}).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := recorder.Only(t)
	if got.Topic != transportmetadata.Topic {
		t.Errorf("topic = %q, want the Kafka topic %q", got.Topic, transportmetadata.Topic)
	}
	if got.Headers[wire.DefaultTopicKey] != "not-the-topic" {
		t.Errorf("headers = %v, want the `topic` header passed through as an ordinary header", got.Headers)
	}
}
