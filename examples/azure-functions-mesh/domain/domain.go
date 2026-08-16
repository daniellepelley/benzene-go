// Package domain holds the trivial handlers examples/azure-functions-mesh deploys across six
// Azure Functions: orders, payments, shipping (the command chain), and inventory, notifications,
// analytics (pure event consumers) - the Go counterpart of benzene-dotnet's own
// examples/AzureFunctionsMesh service handlers (Orders/Domain.cs, Payments/Domain.cs, ...) and
// this repo's own examples/aws-lambda-mesh/domain and examples/k8s-mesh-helloworld/domain, whose
// topic names this topology matches exactly (payment:take, shipment:book - k8s-mesh-helloworld's
// naming - fanned out further over Event Hub/Event Grid, aws-lambda-mesh's naming for that half).
// Handlers are intentionally trivial - this example proves the mesh's transport wiring (Service
// Bus/Event Hub/Event Grid + push fleet reporting), not real business logic, matching how small
// every sibling mesh example's own domain package is.
package domain

import (
	"context"
	"encoding/json"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/mesh"
)

// Topic ids for the estate's five cross-service hops, matching
// examples/azure-functions-mesh/README.md's topology diagram exactly (in turn matching
// benzene-dotnet's examples/AzureFunctionsMesh): payment:take and shipment:book are
// point-to-point commands over Service Bus queues; order:placed is an Event Hub fan-out stream;
// payment:captured and shipment:dispatched are routed Event Grid integration events.
// order:create is orders' own local inbound topic (HTTP only, never crosses a transport).
const (
	TopicOrderCreate        = "order:create"
	TopicPaymentTake        = "payment:take"
	TopicShipmentBook       = "shipment:book"
	TopicOrderPlaced        = "order:placed"
	TopicPaymentCaptured    = "payment:captured"
	TopicShipmentDispatched = "shipment:dispatched"
)

// Service names for the estate's six domain Functions - the mesh.ServiceInfo.Service each
// cmd/<service>/main.go announces itself under, and the selector RegisterOutbound switches on.
// Constants rather than repeated literals so the name a service announces and the outbound
// declaration it picks up can never drift apart.
const (
	ServiceOrders        = "orders"
	ServicePayments      = "payments"
	ServiceShipping      = "shipping"
	ServiceInventory     = "inventory"
	ServiceNotifications = "notifications"
	ServiceAnalytics     = "analytics"
)

// RegisterOutbound declares on outbound every topic service SENDS (mesh.md §2.3) - the
// counterpart to the benzene.Register calls each cmd/<service>/main.go makes for what it
// RECEIVES. Both halves are hard-coded contract, never inferred: this switch is the estate's
// single source for the send side, deliberately kept in the same file as the handlers whose
// bodies do the sending (CreateOrderHandler/TakePaymentHandler/BookShipmentHandler), so a hop
// added to a handler and left undeclared here is visible as a one-file discrepancy. Without it
// the mesh's topic catalog would show every topic with providers and no consumers - a silently
// half-drawn graph, since Descriptor.Consumes, not observed trace parentage, is the sole source
// of a consumer edge (mesh.md §4).
//
// Unlike examples/k8s-mesh-helloworld's equivalent switch, registration here is NOT conditional
// on the matching client.Sender being wired: that example's nil downstream selects a different
// ROLE (one image, three deployments, shipping terminal), whereas each service here is its own
// Function App whose role is fixed at compile time and whose senders differ only by deployment
// wiring. Per mesh.OutboundRegistry's own rule - a declaration carries no destination address,
// so "the descriptor doesn't change between environments, only the wiring does" - an orders
// Function booted with nil senders still declares payment:take and order:placed.
//
// Every hop in this estate is one-way (Service Bus, Event Hub and Event Grid each answer a send
// with an acknowledgement and no payload - see azureservicebus/azureeventhub/azureeventgrid's
// Client.Send), so every record declares TRes as `any`: mesh.md §2.3's "no expected response
// type", which derives the unconstrained {} responseSchema rather than claiming a response shape
// that never comes back.
//
// Returns an error for an unrecognised service - the six names are compile-time fixed, so an
// unknown one is a wiring bug, not a default worth silently absorbing.
func RegisterOutbound(outbound *mesh.OutboundRegistry, service string) error {
	switch service {
	case ServiceOrders:
		// payment:take over a Service Bus queue (the command chain) + order:placed over Event
		// Hub (the fan-out stream) - CreateOrderHandler's two sends.
		if err := mesh.RegisterOutbound[TakePaymentRequest, any](outbound, benzene.NewTopic(TopicPaymentTake)); err != nil {
			return err
		}
		return mesh.RegisterOutbound[OrderPlaced, any](outbound, benzene.NewTopic(TopicOrderPlaced))
	case ServicePayments:
		// shipment:book over Service Bus + payment:captured over Event Grid -
		// TakePaymentHandler's two sends.
		if err := mesh.RegisterOutbound[BookShipmentRequest, any](outbound, benzene.NewTopic(TopicShipmentBook)); err != nil {
			return err
		}
		return mesh.RegisterOutbound[PaymentTaken, any](outbound, benzene.NewTopic(TopicPaymentCaptured))
	case ServiceShipping:
		// Terminal for the command chain (no further Service Bus hop), but it does fan
		// shipment:dispatched out over Event Grid - BookShipmentHandler's one send.
		return mesh.RegisterOutbound[ShipmentBooked, any](outbound, benzene.NewTopic(TopicShipmentDispatched))
	case ServiceInventory, ServiceNotifications, ServiceAnalytics:
		// Pure event consumers: AckHandler sends nothing, so there is nothing to declare. An
		// empty (but present) outbound registry is the honest answer - it leaves Consumes empty
		// without degrading the feed, which is exactly "this service sends nothing" rather than
		// "this service's send side wasn't wired up".
		return nil
	default:
		return fmt.Errorf("domain: unknown service %q", service)
	}
}

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

// TakePaymentRequest is the payment:take request body (payments' Service Bus queue-triggered
// handler).
type TakePaymentRequest struct {
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

// PaymentTaken is both the payment:take response body and the payment:captured event payload
// published to Event Grid - the same shape travels both places, matching how trivially this
// example models its domain.
type PaymentTaken struct {
	PaymentID string  `json:"paymentId"`
	OrderID   string  `json:"orderId"`
	Amount    float64 `json:"amount"`
	Status    string  `json:"status"`
}

// BookShipmentRequest is the shipment:book request body (shipping's Service Bus queue-triggered
// handler).
type BookShipmentRequest struct {
	OrderID string `json:"orderId"`
	Address string `json:"address"`
	Carrier string `json:"carrier"`
}

// ShipmentBooked is both the shipment:book response body and the shipment:dispatched event
// payload published to Event Grid.
type ShipmentBooked struct {
	ShipmentID string `json:"shipmentId"`
	OrderID    string `json:"orderId"`
	Carrier    string `json:"carrier"`
	Status     string `json:"status"`
}

// OrderPlaced is the order:placed event payload streamed over Event Hub - inventory and
// notifications each read it from their own consumer group (a fan-out stream, not a
// point-to-point command).
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
// payment:take over a Service Bus queue (the command chain) and order:placed over Event Hub (the
// fan-out stream). Either sender may be nil - a standalone/unwired run simply skips that hop,
// still answering the caller - matching examples/k8s-mesh-helloworld/domain's degradation rule (a
// send failure is likewise not surfaced to the caller: the order was genuinely created, and
// outbound delivery is this service's own concern to retry/observe via the mesh, not the
// caller's to see fail).
func CreateOrderHandler(payments, orderPlaced client.Sender) benzene.Handler[CreateOrderRequest, OrderCreated] {
	return func(ctx context.Context, req CreateOrderRequest) benzene.Result[OrderCreated] {
		order := OrderCreated{OrderID: "order-1", Status: "created"}

		if payments != nil {
			if body, err := json.Marshal(TakePaymentRequest{OrderID: order.OrderID, Amount: float64(req.Quantity) * 10}); err == nil {
				payments.Send(ctx, benzene.NewTopic(TopicPaymentTake), nil, body)
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

// TakePaymentHandler is payments' only inbound handler, reached over a Service Bus queue
// (payment:take, see cmd/payments). It captures the payment, then chains one hop further to
// shipment:book over Service Bus and fans payment:captured out over Event Grid to notifications
// and analytics.
func TakePaymentHandler(shipping, paymentCaptured client.Sender) benzene.Handler[TakePaymentRequest, PaymentTaken] {
	return func(ctx context.Context, req TakePaymentRequest) benzene.Result[PaymentTaken] {
		payment := PaymentTaken{PaymentID: "pay-1", OrderID: req.OrderID, Amount: req.Amount, Status: "captured"}

		if shipping != nil {
			if body, err := json.Marshal(BookShipmentRequest{OrderID: req.OrderID, Address: "123 Example St", Carrier: "royal-mail"}); err == nil {
				shipping.Send(ctx, benzene.NewTopic(TopicShipmentBook), nil, body)
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

// BookShipmentHandler is shipping's only inbound handler, reached over a Service Bus queue
// (shipment:book, see cmd/shipping). It is the terminal hop of the command chain - no further
// Service Bus send - but fans shipment:dispatched out over Event Grid to inventory,
// notifications, and analytics.
func BookShipmentHandler(shipmentDispatched client.Sender) benzene.Handler[BookShipmentRequest, ShipmentBooked] {
	return func(ctx context.Context, req BookShipmentRequest) benzene.Result[ShipmentBooked] {
		shipment := ShipmentBooked{ShipmentID: "ship-1", OrderID: req.OrderID, Carrier: req.Carrier, Status: "dispatched"}

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
// whichever event payload the caller registers it against (OrderPlaced, PaymentTaken,
// ShipmentBooked); the router's JSON decoding does the real work, this just confirms receipt.
func AckHandler[T any]() benzene.Handler[T, Ack] {
	return func(_ context.Context, _ T) benzene.Result[Ack] {
		return benzene.Ok(Ack{Received: true})
	}
}
