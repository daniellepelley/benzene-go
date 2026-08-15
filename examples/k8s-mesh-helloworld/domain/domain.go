// Package domain holds the three tiny domain handlers the k8s-mesh-helloworld example deploys as
// one shared binary: orders, payments, shipping — the Go counterpart of benzene-dotnet's
// examples/K8sMesh/Service/Domain.cs. Only the handler for the selected MESH_SERVICE is ever
// registered (see cmd/service/main.go), so each deployed Service exposes exactly one domain topic
// and the mesh's fleet view shows real per-service ownership, not three copies of everything.
//
// Handlers are intentionally trivial — this is a demo of service-to-service chaining and mesh
// visibility, not real business logic, matching how small Domain.cs itself is.
package domain

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// Topic ids for the three hops. Namespaced like every other Benzene example topic (no
// benzene:-reserved prefix — these are application topics).
const (
	TopicOrderCreate  = "order:create"
	TopicPaymentTake  = "payment:take"
	TopicShipmentBook = "shipment:book"
)

// CreateOrderRequest is the order:create request body.
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

// TakePaymentRequest is the payment:take request body.
type TakePaymentRequest struct {
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

// PaymentTaken is the payment:take response body.
type PaymentTaken struct {
	PaymentID string `json:"paymentId"`
	Status    string `json:"status"`
}

// BookShipmentRequest is the shipment:book request body.
type BookShipmentRequest struct {
	OrderID string `json:"orderId"`
	Address string `json:"address"`
	Carrier string `json:"carrier"`
}

// ShipmentBooked is the shipment:book response body.
type ShipmentBooked struct {
	ShipmentID string `json:"shipmentId"`
	Status     string `json:"status"`
}

// CreateOrderHandler is the orders service's only handler: it creates the order, then — when
// downstream is non-nil — chains one hop to payments' payment:take over the wire envelope,
// exactly like benzene-dotnet's CreateOrderHandler chains via HttpBenzeneMessageClient. A nil
// downstream (no DOWNSTREAM_MSG_URL configured, e.g. a standalone run) simply skips the chain —
// the handler still answers, it just doesn't propagate.
func CreateOrderHandler(downstream client.Sender) benzene.Handler[CreateOrderRequest, OrderCreated] {
	return func(ctx context.Context, req CreateOrderRequest) benzene.Result[OrderCreated] {
		order := OrderCreated{OrderID: "order-1", Status: "created"}
		if downstream != nil {
			body, err := json.Marshal(TakePaymentRequest{OrderID: order.OrderID, Amount: float64(req.Quantity) * 10})
			if err == nil {
				downstream.Send(ctx, benzene.NewTopic(TopicPaymentTake), nil, body)
			}
		}
		return benzene.Created(order)
	}
}

// TakePaymentHandler is the payments service's only handler: it captures the payment, then —
// when downstream is non-nil — chains one hop to shipping's shipment:book.
func TakePaymentHandler(downstream client.Sender) benzene.Handler[TakePaymentRequest, PaymentTaken] {
	return func(ctx context.Context, req TakePaymentRequest) benzene.Result[PaymentTaken] {
		payment := PaymentTaken{PaymentID: "pay-1", Status: "captured"}
		if downstream != nil {
			body, err := json.Marshal(BookShipmentRequest{OrderID: req.OrderID, Address: "123 Example St", Carrier: "royal-mail"})
			if err == nil {
				downstream.Send(ctx, benzene.NewTopic(TopicShipmentBook), nil, body)
			}
		}
		return benzene.Created(payment)
	}
}

// BookShipmentHandler is the shipping service's only handler. shipping is the terminal hop —
// it has no downstream client at all, matching benzene-dotnet's BookShipmentHandler.
func BookShipmentHandler() benzene.Handler[BookShipmentRequest, ShipmentBooked] {
	return func(_ context.Context, _ BookShipmentRequest) benzene.Result[ShipmentBooked] {
		return benzene.Created(ShipmentBooked{ShipmentID: "ship-1", Status: "booked"})
	}
}
