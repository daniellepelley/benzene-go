// Command sqsworker is the SQS leg of this example: a long-running pod polling one queue,
// dispatching every message into the same greeting.Handler the api pod exposes over HTTP. This is
// the self-hosted awssqs.Consumer (a poll loop this process owns), not the Lambda-trigger
// awssqs.Handler examples/aws-sqs-helloworld uses - the shape a Kubernetes Deployment needs.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/wire"

	"github.com/daniellepelley/benzene-go/examples/k8s-helloworld/greeting"
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func main() {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	queueURL := os.Getenv("QUEUE_URL")
	if queueURL == "" {
		log.Fatal("QUEUE_URL environment variable is not set")
	}

	// SQS_ENDPOINT_URL is only set for a LocalStack/emulator run (see compose/docker-compose.yml);
	// unset (the real-AWS/EKS case), sqs.NewFromConfig uses the SDK's default endpoint resolution -
	// this worker never prescribes how you authenticate, same principle as every other Benzene AWS
	// client on Kubernetes (an IRSA role bound to the pod's ServiceAccount).
	sqsClient := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if endpoint := os.Getenv("SQS_ENDPOINT_URL"); endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})

	consumer := &awssqs.Consumer{
		API:      sqsClient,
		QueueURL: queueURL,
		Builder:  newApp(),
		OnFailure: func(messageID string, resp wire.Response) {
			log.Printf("greet failed [%s]: %s", resp.StatusCode, messageID)
		},
	}
	if err := consumer.Validate(); err != nil {
		log.Fatalf("consumer: %v", err)
	}

	runCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("k8s-helloworld sqsworker polling %s", queueURL)
	if err := consumer.Run(runCtx); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
