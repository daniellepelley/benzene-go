// Command aws-lambda-helloworld is the helloworld service deployed to AWS Lambda: the same
// greet handler, wired through awslambda instead of net/http. Build as `bootstrap` and deploy
// with the `provided.al2023` runtime (see Dockerfile + template.yaml) fronted by a Lambda
// Function URL - no API Gateway resource required.
package main

import (
	"context"
	"encoding/json"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

// newApp is the composition root both main() and the tests boot from.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
				panic(err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

// routes is the HTTP route table both main() and the tests use.
func routes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
}

// newHandler dispatches to awslambda.HTTPHandler for a Function-URL-shaped event (has a
// "requestContext") and awslambda.EnvelopeHandler otherwise - so this one Lambda answers both
// an HTTP caller (curl against the Function URL) and a direct/Lambda-to-Lambda envelope
// invoke, without the caller needing to know which.
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
