// Command aws-lambda-mesh-shipping is the shipping Lambda: triggered by the shipping:book SQS
// queue (payments' outbound send). It books the shipment - the terminal hop of the command chain,
// no further SQS send - and fans shipment:dispatched out over EventBridge to inventory,
// notifications, and analytics.
package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	awslambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awseventbridge"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awslambdaclient"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/meshapp"
)

func newApp(shipmentDispatched client.Sender, meshClient *awslambdaclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: "shipping",
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry) []httpbinding.Route {
			handler := domain.BookShipmentHandler(shipmentDispatched)
			benzene.MustRegister(registry, benzene.NewTopic(domain.TopicShippingBook), handler)
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

func main() {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	eventBridgeClient := awseventbridge.NewClient(eventbridge.NewFromConfig(cfg), "aws-lambda-mesh-shipping")
	eventBridgeClient.EventBusName = requiredEnv("EVENT_BUS_NAME")
	shipmentDispatched := mesh.WithTraceContext(eventBridgeClient)

	var meshClient *awslambdaclient.Client
	if fn := os.Getenv("MESH_FUNCTION_NAME"); fn != "" {
		meshClient = awslambdaclient.NewClient(awslambdasdk.NewFromConfig(cfg), fn)
	}

	app := newApp(shipmentDispatched, meshClient)
	app.Announce(ctx)
	awslambda.Start(app.Handler())
}
