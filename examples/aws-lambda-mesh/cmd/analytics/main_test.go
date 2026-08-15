package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
)

func TestAnalytics_AcknowledgesPaymentCapturedViaEventBridge(t *testing.T) {
	app := newApp(nil)
	event := newEventBridgeEvent(t, domain.TopicPaymentCaptured, domain.PaymentCaptured{PaymentID: "pay-1", OrderID: "order-1"})
	if _, err := app.Handler()(context.Background(), event); err != nil {
		t.Fatalf("handler() error = %v, want nil", err)
	}
}

func TestAnalytics_AcknowledgesShipmentDispatchedViaEventBridge(t *testing.T) {
	app := newApp(nil)
	event := newEventBridgeEvent(t, domain.TopicShipmentDispatched, domain.ShipmentDispatched{ShipmentID: "ship-1", OrderID: "order-1"})
	if _, err := app.Handler()(context.Background(), event); err != nil {
		t.Fatalf("handler() error = %v, want nil", err)
	}
}

func newEventBridgeEvent(t *testing.T, detailType string, payload any) json.RawMessage {
	t.Helper()
	detail, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	event := map[string]any{
		"id":          "evt-1",
		"detail-type": detailType,
		"source":      "test",
		"detail":      json.RawMessage(detail),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return data
}
