// Command aws-lambda-mesh-payments is the payments Lambda: triggered by the payments:capture SQS
// queue (orders' outbound send). It captures the payment, chains one hop further to
// shipping:book over SQS, and fans payment:captured out over EventBridge to analytics and
// notifications - see examples/aws-lambda-mesh/README.md's topology diagram.
package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	awslambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awseventbridge"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awslambdaclient"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/meshapp"
)

func newApp(shipping, paymentCaptured client.Sender, meshClient *awslambdaclient.Client) *meshapp.App {
	return meshapp.New(meshapp.Config{
		ServiceName: "payments",
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry) []httpbinding.Route {
			handler := domain.CapturePaymentHandler(shipping, paymentCaptured)
			if err := benzene.Register(registry, benzene.NewTopic(domain.TopicPaymentsCapture), handler); err != nil {
				log.Fatalf("register %s: %v", domain.TopicPaymentsCapture, err)
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

func main() {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	shipping := mesh.WithTraceContext(awssqs.NewClient(sqs.NewFromConfig(cfg), requiredEnv("SHIPPING_QUEUE_URL")))

	eventBridgeClient := awseventbridge.NewClient(eventbridge.NewFromConfig(cfg), "aws-lambda-mesh-payments")
	eventBridgeClient.EventBusName = requiredEnv("EVENT_BUS_NAME")
	paymentCaptured := mesh.WithTraceContext(eventBridgeClient)

	var meshClient *awslambdaclient.Client
	if fn := os.Getenv("MESH_FUNCTION_NAME"); fn != "" {
		meshClient = awslambdaclient.NewClient(awslambdasdk.NewFromConfig(cfg), fn)
	}

	app := newApp(shipping, paymentCaptured, meshClient)
	app.Announce(ctx)
	awslambda.Start(app.Handler())
}
