// Command gcp-cloudrun-helloworld is the helloworld greet handler deployed to Google Cloud
// Run. Cloud Run's only contract is "listen on $PORT" - which httpbinding.Handler already
// does via ordinary net/http, so this needs no Google-specific package at all, unlike the AWS
// and Azure examples. See README for why Cloud Run (rather than Cloud Functions Gen2, which is
// built on Cloud Run under the hood anyway) is this port's recommended Google Cloud target.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
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

func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	return httpbinding.Handler(builder, routes)
}

// portFromEnv reads $PORT, Cloud Run's documented contract for the port a service must listen
// on - defaulting to 8080 for local runs outside Cloud Run.
func portFromEnv() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func main() {
	handler := newHandler(newApp())
	port := portFromEnv()
	log.Printf("gcp-cloudrun-helloworld listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
