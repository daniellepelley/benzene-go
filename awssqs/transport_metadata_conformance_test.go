package awssqs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
)

// The vendored transport-metadata-cases.json (wire-contracts.md §2) run through this module's two
// inbound bindings: the case's metadata travels as SQS message attributes, and the binding is
// configured the way a service configures it - the reserved names set once on the builder.
var transportMetadataFixture = filepath.Join("..", "conformance", "testdata", transportmetadata.FixtureName)

func TestConformance_TransportMetadata_Handler(t *testing.T) {
	transportmetadata.Run(t, transportMetadataFixture, func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
		attributes := map[string]sqsMessageAttribute{}
		for name, value := range metadata {
			attributes[name] = sqsMessageAttribute{StringValue: value}
		}
		event, err := json.Marshal(sqsEvent{Records: []sqsRecord{{MessageID: "m-1", Body: transportmetadata.Body, MessageAttributes: attributes}}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := Handler(builder)(context.Background(), event); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
}

func TestConformance_TransportMetadata_Consumer(t *testing.T) {
	transportmetadata.Run(t, transportMetadataFixture, func(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
		message := types.Message{
			MessageId:         aws.String("m-1"),
			ReceiptHandle:     aws.String("rh-m-1"),
			Body:              aws.String(transportmetadata.Body),
			MessageAttributes: map[string]types.MessageAttributeValue{},
		}
		for name, value := range metadata {
			message.MessageAttributes[name] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
		}
		consumer := &Consumer{API: &fakeSQS{batches: [][]types.Message{{message}}}, QueueURL: "q", Builder: builder}
		if err := consumer.poll(context.Background()); err != nil {
			t.Fatalf("poll: %v", err)
		}
	})
}
