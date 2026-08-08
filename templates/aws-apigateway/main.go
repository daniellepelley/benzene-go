// Command aws-apigateway is a Benzene service on AWS Lambda, triggered by API Gateway HTTP
// requests - the starter you get from the benzene-go `aws-apigateway` gonew template.
//
// Build it as `bootstrap` for the provided.al2023 custom runtime (see Dockerfile +
// template.yaml). One Lambda answers both an API Gateway HTTP request and a direct
// Lambda-to-Lambda wire-envelope invoke: newHandler dispatches on the event shape, so callers
// that speak either shape reach the same handler.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// greetRequest / greetResponse are the typed message shapes for the demo topic. Replace them
// with your own: a handler deals only in these types and knows nothing about HTTP or Lambda.
type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

// greetHandler is the demo handler - your business logic goes here, not in main(). It resolves
// its one dependency, a Greeter, from the invocation's DI scope (a handler resolves scoped
// dependencies via ScopeFromContext rather than a parameter on its signature - see scope.go's
// ContextWithScope doc comment in benzene-go for why). Add more handlers alongside it and
// register each on its own topic in newApp.
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

// routes is the HTTP route table (path/method -> topic) both main() and the tests use.
func routes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
}

// newHandler dispatches an API Gateway HTTP event (it carries a "requestContext") to
// awslambda.HTTPHandler and a raw wire envelope to awslambda.EnvelopeHandler - so this one
// function serves both an HTTP caller through API Gateway and a direct envelope invoke, without
// the caller needing to know which.
func newHandler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	httpHandler := awslambda.HTTPHandler(builder, routes())
	envelopeHandler := awslambda.EnvelopeHandler(builder)

	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var probe struct {
			RequestContext json.RawMessage `json:"requestContext"`
		}
		if err := json.Unmarshal(event, &probe); err == nil && len(probe.RequestContext) > 0 {
			return httpHandler(ctx, event)
		}
		return envelopeHandler(ctx, event)
	}
}

func main() {
	awslambda.Start(newHandler(newApp().Run()))
}
