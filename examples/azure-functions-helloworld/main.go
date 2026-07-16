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

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

// newHandler builds the custom-handler HTTP server. Route.Path is "/Greet" - the *local*
// invocation path the Functions host uses (the ./Greet function folder's name), independent
// of the "greet" public route configured in Greet/function.json.
func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/Greet", Topic: benzene.NewTopic("greet")}}
	return azurefunctions.Handler(builder, routes)
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
	handler := newHandler(newApp())
	port := portFromEnv()
	log.Printf("azure-functions-helloworld listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
