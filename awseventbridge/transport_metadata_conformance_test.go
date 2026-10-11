package awseventbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
	"github.com/daniellepelley/benzene-go/wire"
)

// TestConformance_TransportMetadata_TopicSource is the eventbridge entry of
// transport-metadata-cases.json's topicSources: the topic is the event's detail-type, and the
// binding "MUST NOT require a topic attribute" - nor read one, embedded or not.
func TestConformance_TransportMetadata_TopicSource(t *testing.T) {
	builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
	event, err := json.Marshal(ruleEvent{
		ID:         "e-1",
		DetailType: transportmetadata.Topic,
		Source:     "conformance",
		Detail:     json.RawMessage(`{"name":"conformance","benzene-headers":{"topic":"not-the-topic"}}`),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := Handler(builder)(context.Background(), event); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := recorder.Only(t).Topic; got != transportmetadata.Topic {
		t.Errorf("topic = %q, want the detail-type %q", got, transportmetadata.Topic)
	}
}
