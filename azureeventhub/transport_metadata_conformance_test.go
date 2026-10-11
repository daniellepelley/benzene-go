package azureeventhub

import (
	"context"
	"path/filepath"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
)

// The vendored transport-metadata-cases.json (wire-contracts.md §2) run through the Consumer: the
// case's metadata travels as the event's application properties, and the reserved names are set
// once on the builder the Consumer inherits them from.
func TestConformance_TransportMetadata_Consumer(t *testing.T) {
	fixture := filepath.Join("..", "conformance", "testdata", transportmetadata.FixtureName)
	transportmetadata.Run(t, fixture, func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
		event := ReceivedEvent{Body: []byte(transportmetadata.Body), Properties: map[string]any{}}
		for name, value := range metadata {
			event.Properties[name] = value
		}
		consumer := &Consumer{Receiver: &fakeReceiver{results: []receiveResult{{events: []ReceivedEvent{event}}}}, Builder: builder}
		_ = consumer.receiveAndDispatch(context.Background()) // an unresolved topic is held for redelivery; the delivery is still recorded
	})
}
