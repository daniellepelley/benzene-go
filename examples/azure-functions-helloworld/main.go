// Command azure-functions-helloworld is the helloworld greet handler deployed as an Azure
// Functions custom handler: a plain HTTP server the Functions host forwards invocations to.
// See ./Greet/function.json for the HTTP trigger binding and ../host.json for the custom
// handler configuration.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/azurefunctions"
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
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

// routes is the HTTP route table both main() and the tests use. Route.Path is "/Greet" - the
// *local* invocation path the Functions host uses (the ./Greet function folder's name),
// independent of the "greet" public route configured in Greet/function.json.
func routes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/Greet", Topic: benzene.NewTopic("greet")}}
}

// newHandler builds the custom-handler HTTP server.
func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return azurefunctions.Handler(builder, routes())
}

// portFromEnv reads FUNCTIONS_CUSTOMHANDLER_PORT, the port the Functions host tells the
// custom handler to listen on - defaulting to 8080, matching the host's own documented default
// for when the variable is absent (e.g. running the binary directly, outside `func start`).
func portFromEnv() string {
	if port := os.Getenv("FUNCTIONS_CUSTOMHANDLER_PORT"); port != "" {
		return port
	}
	return "8080"
}

func main() {
	handler := newHandler(newApp().Run())
	port := portFromEnv()
	log.Printf("azure-functions-helloworld listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
