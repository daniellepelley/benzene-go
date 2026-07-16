package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/daniellepelley/benzene-go/awssqs"
)

type batchResponse struct {
	BatchItemFailures []struct {
		ItemIdentifier string `json:"itemIdentifier"`
	} `json:"batchItemFailures"`
}

func invoke(t *testing.T, event string) batchResponse {
	t.Helper()
	handler := awssqs.Handler(newApp())
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp batchResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v; result = %s", err, result)
	}
	return resp
}

func TestConsumer_ValidGreetMessageSucceeds(t *testing.T) {
	event := `{"Records":[{"messageId":"msg-1","body":"{\"name\":\"World\"}","messageAttributes":{"topic":{"stringValue":"greet"}}}]}`

	resp := invoke(t, event)

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}
}

func TestConsumer_MissingNameIsReportedAsBatchItemFailure(t *testing.T) {
	event := `{"Records":[{"messageId":"msg-1","body":"{\"name\":\"\"}","messageAttributes":{"topic":{"stringValue":"greet"}}}]}`

	resp := invoke(t, event)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
		t.Errorf("BatchItemFailures = %v, want [{msg-1}]", resp.BatchItemFailures)
	}
}
