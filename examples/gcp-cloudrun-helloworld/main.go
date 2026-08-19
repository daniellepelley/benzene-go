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
	benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
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

func main() {
	handler := newHandler(newApp())
	addr := httpbinding.ListenAddr()
	log.Printf("gcp-cloudrun-helloworld listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
