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

func TestCreateOrderHandler_SendsPaymentsCaptureAndOrderPlaced(t *testing.T) {
	payments := &recordingSender{}
	orderPlaced := &recordingSender{}
	handler := CreateOrderHandler(payments, orderPlaced)

	result := handler(context.Background(), CreateOrderRequest{CustomerID: "cust-1", SKU: "espresso", Quantity: 2})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "created" {
		t.Fatalf("result = %+v, want a successful created order", result)
	}
	if len(payments.calls) != 1 || payments.calls[0].topic != benzene.NewTopic(TopicPaymentsCapture) {
		t.Fatalf("payments.calls = %+v, want one send to %s", payments.calls, TopicPaymentsCapture)
	}
	var capture CapturePaymentRequest
	if err := json.Unmarshal(payments.calls[0].message, &capture); err != nil {
		t.Fatalf("unmarshal payments:capture body: %v", err)
	}
	if capture.OrderID != result.Payload.OrderID || capture.Amount != 20 {
		t.Errorf("capture = %+v, want OrderID=%s Amount=20", capture, result.Payload.OrderID)
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

func TestCapturePaymentHandler_SendsShippingBookAndPaymentCaptured(t *testing.T) {
	shipping := &recordingSender{}
	paymentCaptured := &recordingSender{}
	handler := CapturePaymentHandler(shipping, paymentCaptured)

	result := handler(context.Background(), CapturePaymentRequest{OrderID: "order-1", Amount: 20})

	if !result.IsSuccessful() || result.Payload == nil || result.Payload.Status != "captured" {
		t.Fatalf("result = %+v, want a successful captured payment", result)
	}
	if len(shipping.calls) != 1 || shipping.calls[0].topic != benzene.NewTopic(TopicShippingBook) {
		t.Fatalf("shipping.calls = %+v, want one send to %s", shipping.calls, TopicShippingBook)
	}
	var book BookShipmentRequest
	if err := json.Unmarshal(shipping.calls[0].message, &book); err != nil {
		t.Fatalf("unmarshal shipping:book body: %v", err)
	}
	if book.OrderID != "order-1" {
		t.Errorf("book.OrderID = %q, want order-1", book.OrderID)
	}

	if len(paymentCaptured.calls) != 1 || paymentCaptured.calls[0].topic != benzene.NewTopic(TopicPaymentCaptured) {
		t.Fatalf("paymentCaptured.calls = %+v, want one send to %s", paymentCaptured.calls, TopicPaymentCaptured)
	}
	var captured PaymentCaptured
	if err := json.Unmarshal(paymentCaptured.calls[0].message, &captured); err != nil {
		t.Fatalf("unmarshal payment:captured body: %v", err)
	}
	if captured.OrderID != "order-1" || captured.Amount != 20 {
		t.Errorf("captured = %+v, want the payment echoed", captured)
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

	paymentCapturedAck := AckHandler[PaymentCaptured]()
	if r := paymentCapturedAck(context.Background(), PaymentCaptured{}); !r.IsSuccessful() {
		t.Errorf("PaymentCaptured ack result = %+v, want success", r)
	}

	shipmentDispatchedAck := AckHandler[ShipmentDispatched]()
	if r := shipmentDispatchedAck(context.Background(), ShipmentDispatched{}); !r.IsSuccessful() {
		t.Errorf("ShipmentDispatched ack result = %+v, want success", r)
	}
}
