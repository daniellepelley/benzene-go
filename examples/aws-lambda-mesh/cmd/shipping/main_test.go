package main

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
)

type recordingSender struct {
	calls []struct {
		topic   benzene.Topic
		message []byte
	}
}

func (r *recordingSender) Send(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
	r.calls = append(r.calls, struct {
		topic   benzene.Topic
		message []byte
	}{topic, message})
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

func TestShipping_BookViaSQS_FansOutShipmentDispatched(t *testing.T) {
	shipmentDispatched := &recordingSender{}
	app := newApp(shipmentDispatched, nil)

	event := benzenetest.NewSQSEvent(t, "msg-1", benzene.NewTopic(domain.TopicShippingBook),
		domain.BookShipmentRequest{OrderID: "order-1", Address: "x", Carrier: "royal-mail"}, nil)

	raw, err := app.Handler()(context.Background(), event)
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		BatchItemFailures []struct{ ItemIdentifier string } `json:"batchItemFailures"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Fatalf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}

	if len(shipmentDispatched.calls) != 1 || shipmentDispatched.calls[0].topic != benzene.NewTopic(domain.TopicShipmentDispatched) {
		t.Errorf("shipmentDispatched.calls = %+v, want one send to %s", shipmentDispatched.calls, domain.TopicShipmentDispatched)
	}
}

func TestShipping_MissingNameIsReportedAsBatchItemFailure(t *testing.T) {
	// shipping's handler never fails on a well-formed request, but a malformed message body
	// (not valid JSON for BookShipmentRequest) must still surface as a batch item failure, not a
	// panic - proving the composite handler's SQS path preserves awssqs.Handler's own contract.
	app := newApp(nil, nil)
	event := benzenetest.NewSQSEvent(t, "msg-1", benzene.NewTopic(domain.TopicShippingBook), json.RawMessage(`not json`), nil)

	raw, err := app.Handler()(context.Background(), event)
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		BatchItemFailures []struct{ ItemIdentifier string } `json:"batchItemFailures"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
		t.Errorf("BatchItemFailures = %v, want [{msg-1}]", resp.BatchItemFailures)
	}
}
