// Command aws-sqs is a Benzene service on AWS Lambda, triggered by an SQS event source mapping -
// the starter you get from the benzene-go `aws-sqs` gonew template.
//
// Build it as `bootstrap` for the provided.al2023 custom runtime (see Dockerfile +
// template.yaml). awssqs.Handler adapts the Lambda SQS batch payload: it resolves each record's
// topic from its `topic` message attribute (wire-contracts.md §2), runs each through the
// pipeline in its own DI scope, and reports per-message failures back via batchItemFailures so a
// bad message is retried on its own instead of poisoning the whole batch.
package main

import (
	"context"
	"log"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awssqs"
)

// greetRequest / greetResponse are the typed message shapes for the demo topic. Replace them
// with your own: a handler deals only in these types and knows nothing about SQS or Lambda.
type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

// greetHandler is the demo handler - your business logic goes here, not in main(). It resolves
// its one dependency, a Greeter, from the invocation's DI scope (a handler resolves scoped
// dependencies via ScopeFromContext rather than a parameter on its signature - see scope.go's
// ContextWithScope doc comment in benzene-go for why). A non-success Result becomes a reported
// batch-item failure, so the message is redelivered. Add more handlers alongside it and register
// each on its own topic in newApp.
func greetHandler(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}

	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[greetResponse]("no DI scope on context")
	}
	greeter := benzene.GetService[Greeter](scope, greeterKey)

	return benzene.Ok(greetResponse{Greeting: greeter.Greet(req.Name)})
}

// newApp is the composition root: the three-phase benzene.App (core-concepts.md §7) both main()
// and the tests boot from, so a test exercises exactly the wiring that ships.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.AddSingleton(container, greeterKey, func(_ *benzene.Scope) Greeter {
				return helloGreeter{}
			})
			if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
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
