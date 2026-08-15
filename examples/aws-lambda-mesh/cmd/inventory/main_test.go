package main

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
)

func TestInventory_AcknowledgesOrderPlacedViaSNS(t *testing.T) {
	app := newApp(nil)
	event := benzenetest.NewSNSEvent(t, "msg-1", benzene.NewTopic(domain.TopicOrderPlaced),
		domain.OrderPlaced{OrderID: "order-1", CustomerID: "cust-1", SKU: "espresso", Quantity: 2}, nil)

	if _, err := app.Handler()(context.Background(), event); err != nil {
		t.Fatalf("handler() error = %v, want nil (successful SNS notification)", err)
	}
}

func TestInventory_AcknowledgesShipmentDispatchedViaEventBridge(t *testing.T) {
	app := newApp(nil)
	event := newEventBridgeEvent(t, domain.TopicShipmentDispatched,
		domain.ShipmentDispatched{ShipmentID: "ship-1", OrderID: "order-1", Carrier: "royal-mail", Status: "dispatched"})

	if _, err := app.Handler()(context.Background(), event); err != nil {
		t.Fatalf("handler() error = %v, want nil (successful EventBridge event)", err)
	}
}

// newEventBridgeEvent builds the Lambda EventBridge rule-invoke payload awseventbridge.Handler
// parses: top-level "detail-type" + "detail".
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
