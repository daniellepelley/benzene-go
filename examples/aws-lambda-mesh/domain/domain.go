// Package domain holds the trivial handlers examples/aws-lambda-mesh deploys across six Lambdas:
// orders, payments, shipping (the command chain), and inventory, notifications, analytics (pure
// event consumers) - the Go counterpart of benzene-dotnet's examples/AwsMesh service handlers and
// benzene-typescript's examples/aws-lambda-mesh/src/services.ts. Handlers are intentionally
// trivial - this example proves the mesh wiring (SQS/SNS/EventBridge topology + fleet reporting),
// not real business logic, matching how small examples/k8s-mesh-helloworld/domain itself is.
package domain

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// Topic ids for the estate's five cross-service hops, matching
// examples/aws-lambda-mesh/README.md's topology diagram exactly (and, in turn,
// benzene-typescript's examples/aws-lambda-mesh topology): payments:capture and shipping:book are
// point-to-point commands over SQS; order:placed is an SNS fan-out; payment:captured and
// shipment:dispatched are routed EventBridge integration events. order:create is orders' own local
// inbound topic (HTTP only, never crosses a transport).
const (
	TopicOrderCreate        = "order:create"
	TopicPaymentsCapture    = "payments:capture"
	TopicShippingBook       = "shipping:book"
	TopicOrderPlaced        = "order:placed"
	TopicPaymentCaptured    = "payment:captured"
	TopicShipmentDispatched = "shipment:dispatched"
)

// CreateOrderRequest is the order:create request body (orders' native POST /orders route).
type CreateOrderRequest struct {
	CustomerID string `json:"customerId"`
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
}

// OrderCreated is the order:create response body.
type OrderCreated struct {
	OrderID string `json:"orderId"`
	Status  string `json:"status"`
}

// CapturePaymentRequest is the payments:capture request body (payments' SQS-triggered handler).
type CapturePaymentRequest struct {
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

// PaymentCaptured is both the payments:capture response body and the payment:captured event
// payload published to EventBridge - the same shape travels both places, matching how trivially
// this example models its domain.
type PaymentCaptured struct {
	PaymentID string  `json:"paymentId"`
	OrderID   string  `json:"orderId"`
	Amount    float64 `json:"amount"`
	Status    string  `json:"status"`
}

// BookShipmentRequest is the shipping:book request body (shipping's SQS-triggered handler).
type BookShipmentRequest struct {
	OrderID string `json:"orderId"`
	Address string `json:"address"`
	Carrier string `json:"carrier"`
}

// ShipmentDispatched is both the shipping:book response body and the shipment:dispatched event
// payload published to EventBridge.
type ShipmentDispatched struct {
	ShipmentID string `json:"shipmentId"`
	OrderID    string `json:"orderId"`
	Carrier    string `json:"carrier"`
	Status     string `json:"status"`
}

// OrderPlaced is the order:placed event payload published to SNS - inventory and notifications
// both subscribe to it.
type OrderPlaced struct {
	OrderID    string `json:"orderId"`
	CustomerID string `json:"customerId"`
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
}

// Ack is the trivial response every pure-consumer handler (AckHandler) returns.
type Ack struct {
	Received bool `json:"received"`
}

// CreateOrderHandler is orders' only inbound handler, reached over HTTP (POST /orders, see
// cmd/orders). It creates the order, then fans out the two downstream hops the topology draws:
// payments:capture over SQS (the command chain) and order:placed over SNS (the fan-out event).
// Either sender may be nil - a standalone/unwired run simply skips that hop, still answering the
// caller - matching examples/k8s-mesh-helloworld/domain's degradation rule (a send failure is
// likewise not surfaced to the caller: the order was genuinely created, and outbound delivery is
// this service's own concern to retry/observe via the mesh, not the caller's to see fail).
func CreateOrderHandler(payments, orderPlaced client.Sender) benzene.Handler[CreateOrderRequest, OrderCreated] {
	return func(ctx context.Context, req CreateOrderRequest) benzene.Result[OrderCreated] {
		order := OrderCreated{OrderID: "order-1", Status: "created"}

		if payments != nil {
			if body, err := json.Marshal(CapturePaymentRequest{OrderID: order.OrderID, Amount: float64(req.Quantity) * 10}); err == nil {
				payments.Send(ctx, benzene.NewTopic(TopicPaymentsCapture), nil, body)
			}
		}
		if orderPlaced != nil {
			if body, err := json.Marshal(OrderPlaced{OrderID: order.OrderID, CustomerID: req.CustomerID, SKU: req.SKU, Quantity: req.Quantity}); err == nil {
				orderPlaced.Send(ctx, benzene.NewTopic(TopicOrderPlaced), nil, body)
			}
		}

		return benzene.Created(order)
	}
}

// CapturePaymentHandler is payments' only inbound handler, reached over SQS (payments:capture,
// see cmd/payments). It captures the payment, then chains one hop further to shipping:book over
// SQS and fans payment:captured out over EventBridge to analytics + notifications.
func CapturePaymentHandler(shipping, paymentCaptured client.Sender) benzene.Handler[CapturePaymentRequest, PaymentCaptured] {
	return func(ctx context.Context, req CapturePaymentRequest) benzene.Result[PaymentCaptured] {
		payment := PaymentCaptured{PaymentID: "pay-1", OrderID: req.OrderID, Amount: req.Amount, Status: "captured"}

		if shipping != nil {
			if body, err := json.Marshal(BookShipmentRequest{OrderID: req.OrderID, Address: "123 Example St", Carrier: "royal-mail"}); err == nil {
				shipping.Send(ctx, benzene.NewTopic(TopicShippingBook), nil, body)
			}
		}
		if paymentCaptured != nil {
			if body, err := json.Marshal(payment); err == nil {
				paymentCaptured.Send(ctx, benzene.NewTopic(TopicPaymentCaptured), nil, body)
			}
		}

		return benzene.Created(payment)
	}
}

// BookShipmentHandler is shipping's only inbound handler, reached over SQS (shipping:book, see
// cmd/shipping). It is the terminal hop of the command chain - no further SQS send - but fans
// shipment:dispatched out over EventBridge to inventory, notifications, and analytics.
func BookShipmentHandler(shipmentDispatched client.Sender) benzene.Handler[BookShipmentRequest, ShipmentDispatched] {
	return func(ctx context.Context, req BookShipmentRequest) benzene.Result[ShipmentDispatched] {
		shipment := ShipmentDispatched{ShipmentID: "ship-1", OrderID: req.OrderID, Carrier: req.Carrier, Status: "dispatched"}

		if shipmentDispatched != nil {
			if body, err := json.Marshal(shipment); err == nil {
				shipmentDispatched.Send(ctx, benzene.NewTopic(TopicShipmentDispatched), nil, body)
			}
		}

		return benzene.Created(shipment)
	}
}

// AckHandler is the trivial consumer every pure-consumer service (inventory, notifications,
// analytics) registers for each event it subscribes to: acknowledge and do nothing else - the
// point of this example is proving the mesh's transport wiring, not rich domain behaviour. T is
// whichever event payload the caller registers it against (OrderPlaced, PaymentCaptured,
// ShipmentDispatched); the router's JSON decoding does the real work, this just confirms receipt.
func AckHandler[T any]() benzene.Handler[T, Ack] {
	return func(_ context.Context, _ T) benzene.Result[Ack] {
		return benzene.Ok(Ack{Received: true})
	}
}
