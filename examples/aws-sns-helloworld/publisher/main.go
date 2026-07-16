// Command publisher is the HTTP-triggered half of this example: a Lambda function fronted by a
// Function URL that forwards each request onward to SNS instead of processing it locally - the
// consumer Lambda (subscribed to the topic) does the actual work.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awssns"
	"github.com/daniellepelley/benzene-go/httpbinding"

	"github.com/daniellepelley/benzene-go/examples/aws-sns-helloworld/greeting"
)

// forwardHandler builds a handler that publishes req to snsClient under topic "greet" and
// returns whatever outcome the publish itself had (Accepted on success) - it never sees the
// consumer's actual response, since publish and consume happen in two separate Lambda
// invocations connected only by the topic.
func forwardHandler(snsClient *awssns.Client) benzene.Handler[greeting.GreetRequest, struct{}] {
	return func(ctx context.Context, req greeting.GreetRequest) benzene.Result[struct{}] {
		body, err := json.Marshal(req)
		if err != nil {
			return benzene.UnexpectedError[struct{}]("failed to serialize request: " + err.Error())
		}

		result := snsClient.Send(ctx, benzene.NewTopic("greet"), nil, body)
		if !result.Status.IsSuccess() {
			return benzene.Result[struct{}]{Status: result.Status, Errors: result.Errors}
		}
		return benzene.Result[struct{}]{Status: result.Status}
	}
}

func newApp(snsClient *awssns.Client) *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), forwardHandler(snsClient)); err != nil {
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

	topicARN := os.Getenv("TOPIC_ARN")
	if topicARN == "" {
		log.Fatal("TOPIC_ARN environment variable is not set")
	}
	snsClient := awssns.NewClient(sns.NewFromConfig(cfg), topicARN)

	builder := newApp(snsClient)
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	awslambda.Start(awslambda.HTTPHandler(builder, routes))
}
