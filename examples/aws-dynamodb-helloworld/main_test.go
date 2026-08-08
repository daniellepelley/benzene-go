package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/daniellepelley/benzene-go/awsdynamodb"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// These tests boot the real app from its composition root (newApp) and push a native DynamoDB
// stream event in the front door, then assert on the batch-failure response the event source
// mapping reads back. There is no benzenetest.NewDynamoDBStreamEvent helper (the AttributeValue
// image shape is DynamoDB-specific), so the event is hand-built here - the same approach the
// aws-sqs-helloworld consumer's README notes for its own hand-built SQS event.

const ordersArn = "arn:aws:dynamodb:us-east-1:123456789012:table/orders/stream/2024-01-01T00:00:00.000"

// streamRecord builds one DynamoDB stream record for the orders table with the given event name,
// sequence number, and NewImage (in AttributeValue format).
func streamRecord(eventName, seq, newImage string) json.RawMessage {
	return json.RawMessage(`{
		"eventID": "evt-` + seq + `",
		"eventName": "` + eventName + `",
		"eventSource": "aws:dynamodb",
		"eventSourceARN": "` + ordersArn + `",
		"awsRegion": "us-east-1",
		"dynamodb": {"SequenceNumber": "` + seq + `", "StreamViewType": "NEW_AND_OLD_IMAGES", "NewImage": ` + newImage + `}
	}`)
}

func streamEvent(records ...json.RawMessage) json.RawMessage {
	joined := make([]byte, 0)
	for i, r := range records {
		if i > 0 {
			joined = append(joined, ',')
		}
		joined = append(joined, r...)
	}
	return json.RawMessage(`{"Records":[` + string(joined) + `]}`)
}

type batchResponse struct {
	BatchItemFailures []struct {
		ItemIdentifier string `json:"itemIdentifier"`
	} `json:"batchItemFailures"`
}

func handle(t *testing.T, event json.RawMessage) batchResponse {
	t.Helper()
	host := benzenetest.NewHost(newApp())
	out, err := awsdynamodb.Handler(host.Builder())(context.Background(), event)
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	var resp batchResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal batch response %s: %v", out, err)
	}
	return resp
}

func TestOrderInserted_ValidRowReportsNoFailure(t *testing.T) {
	resp := handle(t, streamEvent(
		streamRecord("INSERT", "seq-1", `{"id":{"S":"o-1"},"item":{"S":"widget"},"amount":{"N":"9.99"}}`),
	))
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %+v, want none for a valid order", resp.BatchItemFailures)
	}
}

func TestOrderInserted_MissingIDReportsBatchFailure(t *testing.T) {
	// The handler rejects a row with no id: the record is reported for redelivery by its
	// SequenceNumber, not silently checkpointed past.
	resp := handle(t, streamEvent(
		streamRecord("INSERT", "seq-1", `{"item":{"S":"widget"},"amount":{"N":"9.99"}}`),
	))
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "seq-1" {
		t.Errorf("BatchItemFailures = %+v, want [seq-1]", resp.BatchItemFailures)
	}
}

func TestUnhandledChangeTypeStopsForRedelivery(t *testing.T) {
	// Only INSERT is registered; a MODIFY record routes to no handler (not-found), which for an
	// at-least-once CDC stream is reported for redelivery rather than dropped.
	resp := handle(t, streamEvent(
		streamRecord("MODIFY", "seq-1", `{"id":{"S":"o-1"},"item":{"S":"widget"},"amount":{"N":"9.99"}}`),
	))
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "seq-1" {
		t.Errorf("BatchItemFailures = %+v, want [seq-1] for an unhandled change type", resp.BatchItemFailures)
	}
}
