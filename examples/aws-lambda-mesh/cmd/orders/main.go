// Command aws-lambda-mesh-orders is the orders Lambda: the front door of the estate. It answers
// POST /orders over API Gateway (see examples/aws-lambda-mesh/README.md's topology diagram), then
// fans out two downstream hops - payments:capture over SQS (the command chain) and order:placed
// over SNS (the fan-out event) - and pushes its own register/heartbeat/trace reports to the mesh
// Lambda over a direct Lambda Invoke.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	awslambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awslambdaclient"
	"github.com/daniellepelley/benzene-go/awssns"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/meshapp"
)

// newApp is the composition root both main() and the tests boot from. payments/orderPlaced are
// wrapped in mesh.WithTraceContext by the caller (main), exactly like
// examples/k8s-mesh-helloworld's downstream client - propagation lets the collector show this
// service's declared consumer edges as observed (mesh.md §4.2), on top of the graph itself,
// which comes from a registered ServiceDescriptor.Consumes (mesh.md §4). This example does not
// yet register outbound consumption for payment:capture/order:placed (ROADMAP), so today "orders"
// appears as a provider only on the mesh's topic catalog, not as their declared consumer.
func newApp(payments, orderPlaced client.Sender, meshClient *awslambdaclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: "orders",
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry) []httpbinding.Route {
			handler := domain.CreateOrderHandler(payments, orderPlaced)
			benzene.MustRegister(registry, benzene.NewTopic(domain.TopicOrderCreate), handler)
			return []httpbinding.Route{{Method: http.MethodPost, Path: "/orders", Topic: benzene.NewTopic(domain.TopicOrderCreate)}}
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

	payments := mesh.WithTraceContext(awssqs.NewClient(sqs.NewFromConfig(cfg), requiredEnv("PAYMENTS_QUEUE_URL")))
	orderPlaced := mesh.WithTraceContext(awssns.NewClient(sns.NewFromConfig(cfg), requiredEnv("ORDER_PLACED_TOPIC_ARN")))

	var meshClient *awslambdaclient.Client
	if fn := os.Getenv("MESH_FUNCTION_NAME"); fn != "" {
		meshClient = awslambdaclient.NewClient(awslambdasdk.NewFromConfig(cfg), fn)
	}

	app := newApp(payments, orderPlaced, meshClient)
	app.Announce(ctx)
	awslambda.Start(app.Handler())
}
