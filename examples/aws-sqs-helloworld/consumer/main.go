// Command consumer is the SQS-triggered half of this example: a Lambda function invoked by an
// SQS event source mapping, actually running the greet handler on each message.
package main

import (
	"log"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awssqs"

	"github.com/daniellepelley/benzene-go/examples/aws-sqs-helloworld/greeting"
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	handler := benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)
	if err := benzene.Register(registry, benzene.NewTopic("greet"), handler); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func main() {
	awslambda.Start(awssqs.Handler(newApp()))
}
