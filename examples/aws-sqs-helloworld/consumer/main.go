// Command consumer is the SQS-triggered half of this example: a Lambda function invoked by an
// SQS event source mapping, actually running the greet handler on each message.
package main

import (
	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awssqs"

	"github.com/daniellepelley/benzene-go/examples/aws-sqs-helloworld/greeting"
)

// newApp is the composition root: the three-phase benzene.App both main() and the tests boot
// from, so a test exercises exactly the wiring that ships.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greeting.Handler)
		},
	}
}

func main() {
	awslambda.Start(awssqs.Handler(newApp().Run()))
}
