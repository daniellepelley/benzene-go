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

func TestPayments_CaptureViaSQS_ChainsToShippingAndFansOutEventBridge(t *testing.T) {
	shipping := &recordingSender{}
	paymentCaptured := &recordingSender{}
	app := newApp(shipping, paymentCaptured, nil)

	event := benzenetest.NewSQSEvent(t, "msg-1", benzene.NewTopic(domain.TopicPaymentsCapture),
		domain.CapturePaymentRequest{OrderID: "order-1", Amount: 20}, nil)

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

	if len(shipping.calls) != 1 || shipping.calls[0].topic != benzene.NewTopic(domain.TopicShippingBook) {
		t.Errorf("shipping.calls = %+v, want one send to %s", shipping.calls, domain.TopicShippingBook)
	}
	if len(paymentCaptured.calls) != 1 || paymentCaptured.calls[0].topic != benzene.NewTopic(domain.TopicPaymentCaptured) {
		t.Errorf("paymentCaptured.calls = %+v, want one send to %s", paymentCaptured.calls, domain.TopicPaymentCaptured)
	}
}
