package azureservicebus

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
)

// The vendored transport-metadata-cases.json (wire-contracts.md §2) run through the self-hosted
// Worker: the case's metadata travels as the message's application properties, and the reserved
// names are set once on the builder the Worker inherits them from.
func TestConformance_TransportMetadata_Worker(t *testing.T) {
	fixture := filepath.Join("..", "conformance", "testdata", transportmetadata.FixtureName)
	transportmetadata.Run(t, fixture, func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
		message := &azservicebus.ReceivedMessage{MessageID: "m-1", Body: []byte(transportmetadata.Body), ApplicationProperties: map[string]any{}}
		for name, value := range metadata {
			message.ApplicationProperties[name] = value
		}
		worker := &Worker{API: &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{message}}}, Builder: builder}
		if err := worker.poll(context.Background()); err != nil {
			t.Fatalf("poll: %v", err)
		}
	})
}
