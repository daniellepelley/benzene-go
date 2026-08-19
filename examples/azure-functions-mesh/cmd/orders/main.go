// Command azure-functions-mesh-orders is the orders Azure Function: the front door of the
// estate. It answers POST /orders (see examples/azure-functions-mesh/README.md's topology
// diagram), then fans out two downstream hops - payment:take over a Service Bus queue (the
// command chain) and order:placed over Event Hub (the fan-out stream) - and pushes its own
// register/heartbeat/trace reports to the mesh Function over plain HTTP.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/azureeventhub"
	"github.com/daniellepelley/benzene-go/azureservicebus"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"

	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/meshapp"
)

// newApp is the composition root both main() and the tests boot from. payments/orderPlaced are
// wrapped in mesh.WithTraceContext by the caller (main), exactly like every other mesh example's
// downstream client - propagation lets the collector show this service's declared consumer edges
// as observed (mesh.md §4.2), on top of the graph itself, which comes from the registered
// ServiceDescriptor.Produces declared below (mesh.md §4) and from nothing else.
func newApp(payments, orderPlaced client.Sender, meshClient *httpclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: domain.ServiceOrders,
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry, outbound *mesh.OutboundRegistry) []httpbinding.Route {
			handler := domain.CreateOrderHandler(payments, orderPlaced)
			benzene.MustRegister(registry, benzene.NewTopic(domain.TopicOrderCreate), handler)
			// What this service SENDS: payment:take (Service Bus) + order:placed (Event Hub).
			if err := domain.RegisterOutbound(outbound, domain.ServiceOrders); err != nil {
				log.Fatalf("register outbound for %s: %v", domain.ServiceOrders, err)
			}
			return []httpbinding.Route{{Method: http.MethodPost, Path: "/Orders", Topic: benzene.NewTopic(domain.TopicOrderCreate)}}
		},
	})
}

func requiredEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s environment variable is not set", name)
	}
	return v
}

// mux builds the custom-handler HTTP server: one local path per Function folder
// (Spec/Health/Orders share app.HTTPHandler's Route-table dispatch; Invoke needs its own
// envelope adapter - see meshapp/httpadapters.go's package doc).
func mux(app *meshapp.App) http.Handler {
	m := http.NewServeMux()
	httpHandler := app.HTTPHandler()
	m.Handle("/Spec", httpHandler)
	m.Handle("/Health", httpHandler)
	m.Handle("/Orders", httpHandler)
	m.Handle("/Invoke", app.EnvelopeHandler())
	return m
}

func main() {
	ctx := context.Background()

	sbClient, err := azservicebus.NewClientFromConnectionString(requiredEnv("ServiceBusConnection"), nil)
	if err != nil {
		log.Fatalf("service bus client: %v", err)
	}
	paymentsSender, err := sbClient.NewSender(requiredEnv("PAYMENTS_QUEUE"), nil)
	if err != nil {
		log.Fatalf("service bus sender: %v", err)
	}
	payments := mesh.WithTraceContext(azureservicebus.NewClient(paymentsSender))

	ehProducer, err := azeventhubs.NewProducerClientFromConnectionString(requiredEnv("EventHubConnection"), requiredEnv("ORDER_PLACED_HUB"), nil)
	if err != nil {
		log.Fatalf("event hub producer: %v", err)
	}
	orderPlaced := mesh.WithTraceContext(azureeventhub.NewClient(azureeventhub.NewProducerAdapter(ehProducer)))

	var meshClient *httpclient.Client
	if url := os.Getenv("MESH_INVOKE_URL"); url != "" {
		meshClient = httpclient.NewClient(url)
	}

	app := newApp(payments, orderPlaced, meshClient)
	defer app.Close()

	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go app.RunHeartbeatLoop(heartbeatCtx)

	addr := azurefunctions.ListenAddr()
	log.Printf("orders listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux(app)))
}
