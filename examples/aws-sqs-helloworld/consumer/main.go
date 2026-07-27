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

// newApp is the composition root: the three-phase benzene.App both main() and the tests boot
// from, so a test exercises exactly the wiring that ships.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			handler := benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)
			if err := benzene.Register(registry, benzene.NewTopic("greet"), handler); err != nil {
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

func main() {
	awslambda.Start(awssqs.Handler(newApp().Run()))
}
