package awssns

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
)

// The vendored transport-metadata-cases.json (wire-contracts.md §2) run through Handler: the case's
// metadata travels as SNS message attributes, and the reserved names are set once on the builder.
func TestConformance_TransportMetadata_Handler(t *testing.T) {
	fixture := filepath.Join("..", "conformance", "testdata", transportmetadata.FixtureName)
	transportmetadata.Run(t, fixture, func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
		attributes := map[string]snsMessageAttribute{}
		for name, value := range metadata {
			attributes[name] = snsMessageAttribute{Value: value}
		}
		event, err := json.Marshal(snsEvent{Records: []snsRecord{{Sns: snsMessage{MessageID: "m-1", Message: transportmetadata.Body, MessageAttributes: attributes}}}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		_, _ = Handler(builder)(context.Background(), event) // an unresolved topic fails the notification; the delivery is still recorded
	})
}
