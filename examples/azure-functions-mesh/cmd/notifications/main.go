// Command azure-functions-mesh-notifications is the notifications Azure Function - a pure event
// consumer. It reads order:placed off its own Event Hub consumer group (see
// examples/azure-functions-mesh/README.md's topology diagram) and both payment:captured and
// shipment:dispatched off ONE Event Grid subscription (a single subscription with both event
// types included routes both to the same Function - see ../../deploy/main.tf). Handlers are
// trivial acknowledgements (domain.AckHandler) - the point of this example is proving the mesh's
// transport wiring, not rich domain behaviour.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"

	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/meshapp"
)

// newApp is the composition root both main() and the tests boot from.
func newApp(meshClient *httpclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: domain.ServiceNotifications,
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry, outbound *mesh.OutboundRegistry) []httpbinding.Route {
			benzene.MustRegister(registry, benzene.NewTopic(domain.TopicOrderPlaced), domain.AckHandler[domain.OrderPlaced]())
			benzene.MustRegister(registry, benzene.NewTopic(domain.TopicPaymentCaptured), domain.AckHandler[domain.PaymentTaken]())
			benzene.MustRegister(registry, benzene.NewTopic(domain.TopicShipmentDispatched), domain.AckHandler[domain.ShipmentBooked]())
			// Declares nothing outbound - a pure event consumer - but every service routes its
			// send side through the same call, so the day this one gains a hop there is exactly
			// one place to declare it.
			if err := domain.RegisterOutbound(outbound, domain.ServiceNotifications); err != nil {
				log.Fatalf("register outbound for %s: %v", domain.ServiceNotifications, err)
			}
			return nil
		},
	})
}

func mux(app *meshapp.App) http.Handler {
	m := http.NewServeMux()
	httpHandler := app.HTTPHandler()
	m.Handle("/Spec", httpHandler)
	m.Handle("/Health", httpHandler)
	m.Handle("/Invoke", app.EnvelopeHandler())
	m.Handle("/OrderPlaced", azurefunctions.EventHubHandler(app.Builder(), "eventHubMessages"))
	m.Handle("/IntegrationEvents", azurefunctions.EventGridHandler(app.Builder(), "eventGridEvent"))
	return m
}

func main() {
	var meshClient *httpclient.Client
	if url := os.Getenv("MESH_INVOKE_URL"); url != "" {
		meshClient = httpclient.NewClient(url)
	}

	app := newApp(meshClient)
	defer app.Close()

	heartbeatCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.RunHeartbeatLoop(heartbeatCtx)

	addr := azurefunctions.ListenAddr()
	log.Printf("notifications listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux(app)))
}
