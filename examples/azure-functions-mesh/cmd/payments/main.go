// Command azure-functions-mesh-payments is the payments Azure Function. It is triggered by
// Service Bus messages on the "payments" queue (payment:take, see
// examples/azure-functions-mesh/README.md's topology diagram), captures the payment, then chains
// one hop further to shipment:book over a Service Bus queue and fans payment:captured out over
// Event Grid to notifications and analytics.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/azureeventgrid"
	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/azureservicebus"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"

	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/meshapp"
)

// newApp is the composition root both main() and the tests boot from.
func newApp(shipping, paymentCaptured client.Sender, meshClient *httpclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: domain.ServicePayments,
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry, outbound *mesh.OutboundRegistry) []httpbinding.Route {
			handler := domain.TakePaymentHandler(shipping, paymentCaptured)
			if err := benzene.Register(registry, benzene.NewTopic(domain.TopicPaymentTake), handler); err != nil {
				log.Fatalf("register %s: %v", domain.TopicPaymentTake, err)
			}
			// What this service SENDS: shipment:book (Service Bus) + payment:captured (Event Grid).
			if err := domain.RegisterOutbound(outbound, domain.ServicePayments); err != nil {
				log.Fatalf("register outbound for %s: %v", domain.ServicePayments, err)
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

func portFromEnv() string {
	if port := os.Getenv("FUNCTIONS_CUSTOMHANDLER_PORT"); port != "" {
		return port
	}
	return "8080"
}

// mux builds the custom-handler HTTP server (Spec/Health/Invoke) plus the Service Bus
// queue-trigger local path (PaymentTake). The Service Bus binding name below ("mySbMsg") must
// match PaymentTake/function.json's own trigger "name".
func mux(app *meshapp.App) http.Handler {
	m := http.NewServeMux()
	httpHandler := app.HTTPHandler()
	m.Handle("/Spec", httpHandler)
	m.Handle("/Health", httpHandler)
	m.Handle("/Invoke", app.EnvelopeHandler())
	m.Handle("/PaymentTake", azurefunctions.QueueHandler(app.Builder(), "mySbMsg"))
	return m
}

func main() {
	ctx := context.Background()

	sbClient, err := azservicebus.NewClientFromConnectionString(requiredEnv("ServiceBusConnection"), nil)
	if err != nil {
		log.Fatalf("service bus client: %v", err)
	}
	shippingSender, err := sbClient.NewSender(requiredEnv("SHIPPING_QUEUE"), nil)
	if err != nil {
		log.Fatalf("service bus sender: %v", err)
	}
	shipping := mesh.WithTraceContext(azureservicebus.NewClient(shippingSender))

	egClient, err := azeventgrid.NewClientWithSharedKeyCredential(requiredEnv("EventGridEndpoint"), azcore.NewKeyCredential(requiredEnv("EventGridKey")), nil)
	if err != nil {
		log.Fatalf("event grid client: %v", err)
	}
	paymentCaptured := mesh.WithTraceContext(azureeventgrid.NewClient(egClient, "com.example.payments"))

	var meshClient *httpclient.Client
	if url := os.Getenv("MESH_INVOKE_URL"); url != "" {
		meshClient = httpclient.NewClient(url)
	}

	app := newApp(shipping, paymentCaptured, meshClient)
	defer app.Close()

	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go app.RunHeartbeatLoop(heartbeatCtx)

	port := portFromEnv()
	log.Printf("payments listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux(app)))
}
