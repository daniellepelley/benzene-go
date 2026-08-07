// Command publisher is the HTTP-triggered half of this example: a Lambda function fronted by a
// Function URL that forwards each request onward to SQS instead of processing it locally - the
// consumer Lambda (triggered by the queue) does the actual work.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"

	"github.com/daniellepelley/benzene-go/examples/aws-sqs-helloworld/greeting"
)

// forwardHandler publishes req to the outbound Sender (resolved from the invocation scope) under
// topic "greet" and returns whatever outcome the publish itself had (Accepted on success) - it
// never sees the consumer's actual response, since publish and consume happen in two separate
// Lambda invocations connected only by the queue. Resolving the Sender from the container -
// rather than closing over the concrete client - is what lets a test swap in a
// benzenetest.FakeMessageSender via WithServices and assert on the egress.
func forwardHandler() benzene.Handler[greeting.GreetRequest, struct{}] {
	return func(ctx context.Context, req greeting.GreetRequest) benzene.Result[struct{}] {
		scope, ok := benzene.ScopeFromContext(ctx)
		if !ok {
			return benzene.UnexpectedError[struct{}]("no DI scope on context")
		}
		body, err := json.Marshal(req)
		if err != nil {
			return benzene.UnexpectedError[struct{}]("failed to serialize request: " + err.Error())
		}

		result := client.SenderFromScope(scope).Send(ctx, benzene.NewTopic("greet"), nil, body)
		if !result.IsSuccessful() {
			return benzene.Result[struct{}]{Status: result.Status, Errors: result.Errors}
		}
		return benzene.Result[struct{}]{Status: result.Status}
	}
}

// newApp is the composition root both main() and the tests boot from. It registers the outbound
// Sender in the container so the egress edge is swappable; main wires the real SQS client,
// a test wires a fake.
func newApp(sender client.Sender) benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			client.RegisterSender(container, sender)
			if err := benzene.Register(registry, benzene.NewTopic("greet"), forwardHandler()); err != nil {
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

// routes is the HTTP route table both main() and the tests use, so a test declares the same
// front door the deployment exposes.
func routes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
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
	sqsClient := awssqs.NewClient(sqs.NewFromConfig(cfg), queueURL)

	builder := newApp(sqsClient).Run()
	awslambda.Start(awslambda.HTTPHandler(builder, routes()))
}
