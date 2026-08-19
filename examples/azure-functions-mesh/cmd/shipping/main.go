// Command azure-functions-mesh-shipping is the shipping Azure Function. It is triggered by
// Service Bus messages on the "shipping" queue (shipment:book, see
// examples/azure-functions-mesh/README.md's topology diagram) - the terminal hop of the command
// chain, no further Service Bus send - and fans shipment:dispatched out over Event Grid to
// inventory, notifications, and analytics.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/azureeventgrid"
	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"

	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/meshapp"
)

// newApp is the composition root both main() and the tests boot from.
func newApp(shipmentDispatched client.Sender, meshClient *httpclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: domain.ServiceShipping,
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry, outbound *mesh.OutboundRegistry) []httpbinding.Route {
			handler := domain.BookShipmentHandler(shipmentDispatched)
			benzene.MustRegister(registry, benzene.NewTopic(domain.TopicShipmentBook), handler)
			// What this service SENDS: shipment:dispatched (Event Grid) - terminal for the
			// command chain, but still a publisher of its own integration event.
			if err := domain.RegisterOutbound(outbound, domain.ServiceShipping); err != nil {
				log.Fatalf("register outbound for %s: %v", domain.ServiceShipping, err)
			}
			return nil
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

func mux(app *meshapp.App) http.Handler {
	m := http.NewServeMux()
	httpHandler := app.HTTPHandler()
	m.Handle("/Spec", httpHandler)
	m.Handle("/Health", httpHandler)
	m.Handle("/Invoke", app.EnvelopeHandler())
	m.Handle("/ShipmentBook", azurefunctions.QueueHandler(app.Builder(), "mySbMsg"))
	return m
}

func main() {
	ctx := context.Background()

	egClient, err := azeventgrid.NewClientWithSharedKeyCredential(requiredEnv("EventGridEndpoint"), azcore.NewKeyCredential(requiredEnv("EventGridKey")), nil)
	if err != nil {
		log.Fatalf("event grid client: %v", err)
	}
	shipmentDispatched := mesh.WithTraceContext(azureeventgrid.NewClient(egClient, "com.example.shipping"))

	var meshClient *httpclient.Client
	if url := os.Getenv("MESH_INVOKE_URL"); url != "" {
		meshClient = httpclient.NewClient(url)
	}

	app := newApp(shipmentDispatched, meshClient)
	defer app.Close()

	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go app.RunHeartbeatLoop(heartbeatCtx)

	addr := azurefunctions.ListenAddr()
	log.Printf("shipping listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux(app)))
}
