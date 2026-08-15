package domain

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// recordingSender captures every Send call, standing in for a downstream client.Sender in
// isolation from any real transport.
type recordingSender struct {
	topic   benzene.Topic
	body    []byte
	called  bool
	respond benzene.Result[json.RawMessage]
}

func (s *recordingSender) Send(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
	s.called = true
	s.topic = topic
	s.body = message
	return s.respond
}

var _ client.Sender = (*recordingSender)(nil)

func TestCreateOrderHandler_ChainsToPaymentTake(t *testing.T) {
	sender := &recordingSender{respond: benzene.Ok[json.RawMessage](nil)}
	handler := CreateOrderHandler(sender)

	result := handler(context.Background(), CreateOrderRequest{CustomerID: "c1", SKU: "espresso", Quantity: 2})

	if !result.IsSuccessful() || result.Payload == nil {
		t.Fatalf("result = %+v, want a successful OrderCreated", result)
	}
	if result.Payload.Status != "created" {
		t.Errorf("Status = %q, want created", result.Payload.Status)
	}
	if !sender.called || sender.topic.ID != TopicPaymentTake {
		t.Fatalf("downstream called=%v topic=%v, want a call to %s", sender.called, sender.topic, TopicPaymentTake)
	}
	var payment TakePaymentRequest
	if err := json.Unmarshal(sender.body, &payment); err != nil {
		t.Fatalf("unmarshal chained payload: %v", err)
	}
	if payment.OrderID != result.Payload.OrderID || payment.Amount != 20 {
		t.Errorf("payment = %+v, want OrderID=%s Amount=20", payment, result.Payload.OrderID)
	}
}

func TestCreateOrderHandler_NilDownstreamStillAnswers(t *testing.T) {
	handler := CreateOrderHandler(nil)
	result := handler(context.Background(), CreateOrderRequest{Quantity: 1})
	if !result.IsSuccessful() {
		t.Fatalf("result = %+v, want success with no downstream configured", result)
	}
}

func TestTakePaymentHandler_ChainsToShipmentBook(t *testing.T) {
	sender := &recordingSender{respond: benzene.Ok[json.RawMessage](nil)}
	handler := TakePaymentHandler(sender)

	result := handler(context.Background(), TakePaymentRequest{OrderID: "order-1", Amount: 20})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "captured" {
		t.Fatalf("result = %+v, want a captured PaymentTaken", result)
	}
	if !sender.called || sender.topic.ID != TopicShipmentBook {
		t.Fatalf("downstream called=%v topic=%v, want a call to %s", sender.called, sender.topic, TopicShipmentBook)
	}
	var shipment BookShipmentRequest
	if err := json.Unmarshal(sender.body, &shipment); err != nil {
		t.Fatalf("unmarshal chained payload: %v", err)
	}
	if shipment.OrderID != "order-1" {
		t.Errorf("shipment.OrderID = %q, want order-1", shipment.OrderID)
	}
}

func TestBookShipmentHandler_Terminal(t *testing.T) {
	handler := BookShipmentHandler()
	result := handler(context.Background(), BookShipmentRequest{OrderID: "order-1"})
	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "booked" {
		t.Fatalf("result = %+v, want a booked ShipmentBooked", result)
	}
}
