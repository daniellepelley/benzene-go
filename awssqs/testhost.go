package awssqs

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// SQSResponse is the native Lambda partial-batch-failure report SendSQS decodes back for
// assertion - the same shape awssqs.Handler emits. An empty BatchItemFailures means every record
// in the batch was handled successfully.
type SQSResponse struct {
	BatchItemFailures []SQSBatchItemFailure `json:"batchItemFailures"`
}

// SQSBatchItemFailure identifies a record the event source mapping should redeliver.
type SQSBatchItemFailure struct {
	ItemIdentifier string `json:"itemIdentifier"`
}

// TestMessageID is the message ID SendSQS stamps on the single record it delivers, so a test can
// assert a batch item failure names it.
const TestMessageID = "benzenetest-sqs-1"

// SendSQS pushes an SQS event-source-mapping delivery for topic+payload (with optional headers)
// through the real awssqs binding on host, and returns the native batch-failure response. This
// is the SQS specialization+dispatch in one call - the Go counterpart of the reference
// AwsEventBuilder.CreateSqsEvent + SendEventAsync. It is parallel in name, argument order, and
// return shape with every other Send* (SendSNS, benzenetest.SendPubSub, ...): only the call name
// changes between transports.
func SendSQS(t benzenetest.TB, host *benzenetest.Host, topic benzene.Topic, payload any, headers map[string]string) SQSResponse {
	t.Helper()
	event := benzenetest.NewSQSEvent(t, TestMessageID, topic, payload, headers)
	raw, err := Handler(host.Builder())(context.Background(), event)
	if err != nil {
		t.Fatalf("awssqs: SendSQS dispatch error: %v", err)
	}
	var resp SQSResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("awssqs: decode SQS batch response: %v; response = %s", err, raw)
	}
	return resp
}
