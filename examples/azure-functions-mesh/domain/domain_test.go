package domain

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// recordingSender is a minimal client.Sender fake that records every Send call - enough to assert
// which topic a handler published to and what it sent, with no real transport involved.
type recordingSender struct {
	calls []recordedSend
}

type recordedSend struct {
	topic   benzene.Topic
	message []byte
}

func (r *recordingSender) Send(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
	r.calls = append(r.calls, recordedSend{topic: topic, message: message})
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

func TestCreateOrderHandler_SendsPaymentTakeAndOrderPlaced(t *testing.T) {
	payments := &recordingSender{}
	orderPlaced := &recordingSender{}
	handler := CreateOrderHandler(payments, orderPlaced)

	result := handler(context.Background(), CreateOrderRequest{CustomerID: "cust-1", SKU: "espresso", Quantity: 2})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "created" {
		t.Fatalf("result = %+v, want a successful created order", result)
	}
	if len(payments.calls) != 1 || payments.calls[0].topic != benzene.NewTopic(TopicPaymentTake) {
		t.Fatalf("payments.calls = %+v, want one send to %s", payments.calls, TopicPaymentTake)
	}
	var take TakePaymentRequest
	if err := json.Unmarshal(payments.calls[0].message, &take); err != nil {
		t.Fatalf("unmarshal payment:take body: %v", err)
	}
	if take.OrderID != result.Payload.OrderID || take.Amount != 20 {
		t.Errorf("take = %+v, want OrderID=%s Amount=20", take, result.Payload.OrderID)
	}

	if len(orderPlaced.calls) != 1 || orderPlaced.calls[0].topic != benzene.NewTopic(TopicOrderPlaced) {
		t.Fatalf("orderPlaced.calls = %+v, want one send to %s", orderPlaced.calls, TopicOrderPlaced)
	}
	var placed OrderPlaced
	if err := json.Unmarshal(orderPlaced.calls[0].message, &placed); err != nil {
		t.Fatalf("unmarshal order:placed body: %v", err)
	}
	if placed.CustomerID != "cust-1" || placed.SKU != "espresso" || placed.Quantity != 2 {
		t.Errorf("placed = %+v, want the request echoed", placed)
	}
}

func TestCreateOrderHandler_NilSendersStillAnswers(t *testing.T) {
	handler := CreateOrderHandler(nil, nil)

	result := handler(context.Background(), CreateOrderRequest{CustomerID: "cust-1", SKU: "sku", Quantity: 1})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "created" {
		t.Fatalf("result = %+v, want a successful created order even with no downstream wired", result)
	}
}

func TestTakePaymentHandler_SendsShipmentBookAndPaymentCaptured(t *testing.T) {
	shipping := &recordingSender{}
	paymentCaptured := &recordingSender{}
	handler := TakePaymentHandler(shipping, paymentCaptured)

	result := handler(context.Background(), TakePaymentRequest{OrderID: "order-1", Amount: 20})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "captured" {
		t.Fatalf("result = %+v, want a successful captured payment", result)
	}
	if len(shipping.calls) != 1 || shipping.calls[0].topic != benzene.NewTopic(TopicShipmentBook) {
		t.Fatalf("shipping.calls = %+v, want one send to %s", shipping.calls, TopicShipmentBook)
	}
	var book BookShipmentRequest
	if err := json.Unmarshal(shipping.calls[0].message, &book); err != nil {
		t.Fatalf("unmarshal shipment:book body: %v", err)
	}
	if book.OrderID != "order-1" {
		t.Errorf("book.OrderID = %q, want order-1", book.OrderID)
	}

	if len(paymentCaptured.calls) != 1 || paymentCaptured.calls[0].topic != benzene.NewTopic(TopicPaymentCaptured) {
		t.Fatalf("paymentCaptured.calls = %+v, want one send to %s", paymentCaptured.calls, TopicPaymentCaptured)
	}
	var captured PaymentTaken
	if err := json.Unmarshal(paymentCaptured.calls[0].message, &captured); err != nil {
		t.Fatalf("unmarshal payment:captured body: %v", err)
	}
	if captured.OrderID != "order-1" || captured.Amount != 20 {
		t.Errorf("captured = %+v, want the payment echoed", captured)
	}
}

func TestTakePaymentHandler_NilSendersStillAnswers(t *testing.T) {
	handler := TakePaymentHandler(nil, nil)

	result := handler(context.Background(), TakePaymentRequest{OrderID: "order-1", Amount: 20})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "captured" {
		t.Fatalf("result = %+v, want a successful captured payment even with no downstream wired", result)
	}
}

func TestBookShipmentHandler_SendsShipmentDispatchedAndHasNoFurtherHop(t *testing.T) {
	shipmentDispatched := &recordingSender{}
	handler := BookShipmentHandler(shipmentDispatched)

	result := handler(context.Background(), BookShipmentRequest{OrderID: "order-1", Address: "x", Carrier: "royal-mail"})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "dispatched" {
		t.Fatalf("result = %+v, want a successful dispatched shipment", result)
	}
	if len(shipmentDispatched.calls) != 1 || shipmentDispatched.calls[0].topic != benzene.NewTopic(TopicShipmentDispatched) {
		t.Fatalf("shipmentDispatched.calls = %+v, want one send to %s", shipmentDispatched.calls, TopicShipmentDispatched)
	}
}

func TestBookShipmentHandler_NilSenderStillAnswers(t *testing.T) {
	handler := BookShipmentHandler(nil)

	result := handler(context.Background(), BookShipmentRequest{OrderID: "order-1"})

	if !result.IsSuccessful() {
		t.Fatalf("result = %+v, want success even with no downstream wired (shipping is terminal)", result)
	}
}

func TestAckHandler_AcknowledgesWhateverItsRegisteredPayloadType(t *testing.T) {
	orderPlacedAck := AckHandler[OrderPlaced]()
	result := orderPlacedAck(context.Background(), OrderPlaced{OrderID: "order-1"})
	if !result.IsSuccessful() || result.Payload == nil || !result.Payload.Received {
		t.Fatalf("result = %+v, want a successful Ack{Received:true}", result)
	}

	paymentTakenAck := AckHandler[PaymentTaken]()
	if r := paymentTakenAck(context.Background(), PaymentTaken{}); !r.IsSuccessful() {
		t.Errorf("PaymentTaken ack result = %+v, want success", r)
	}

	shipmentBookedAck := AckHandler[ShipmentBooked]()
	if r := shipmentBookedAck(context.Background(), ShipmentBooked{}); !r.IsSuccessful() {
		t.Errorf("ShipmentBooked ack result = %+v, want success", r)
	}
}
