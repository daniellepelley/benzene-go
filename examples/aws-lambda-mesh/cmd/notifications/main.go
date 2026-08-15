// Command aws-lambda-mesh-notifications is the notifications Lambda: a pure event consumer
// subscribed to order:placed (SNS), payment:captured (EventBridge), and shipment:dispatched
// (EventBridge) - the busiest fan-in in the estate (see
// examples/aws-lambda-mesh/README.md's topology diagram). It has no outbound sends; its only job
// is to acknowledge (domain.AckHandler) and report itself to the mesh.
package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	awslambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awslambdaclient"
	"github.com/daniellepelley/benzene-go/httpbinding"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/meshapp"
)

func newApp(meshClient *awslambdaclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: "notifications",
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry) []httpbinding.Route {
			mustRegister(registry, domain.TopicOrderPlaced, domain.AckHandler[domain.OrderPlaced]())
			mustRegister(registry, domain.TopicPaymentCaptured, domain.AckHandler[domain.PaymentCaptured]())
			mustRegister(registry, domain.TopicShipmentDispatched, domain.AckHandler[domain.ShipmentDispatched]())
			return nil
		},
	})
}

func mustRegister[TReq, TRes any](registry *benzene.Registry, topic string, handler benzene.Handler[TReq, TRes]) {
	if err := benzene.Register(registry, benzene.NewTopic(topic), handler); err != nil {
		log.Fatalf("register %s: %v", topic, err)
	}
}

func main() {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	var meshClient *awslambdaclient.Client
	if fn := os.Getenv("MESH_FUNCTION_NAME"); fn != "" {
		meshClient = awslambdaclient.NewClient(awslambdasdk.NewFromConfig(cfg), fn)
	}

	app := newApp(meshClient)
	app.Announce(ctx)
	awslambda.Start(app.Handler())
}
