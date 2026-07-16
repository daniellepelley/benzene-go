// Command gcp-pubsub-helloworld is the helloworld greet handler consuming a Google Cloud
// Pub/Sub push subscription, deployed as a Cloud Run service. A push subscription delivers
// each message as an HTTPS POST to this service (no polling, no SDK), so the whole consumer
// is gcppubsub.Handler mounted on a route - publishing needs no code at all, just
// `gcloud pubsub topics publish` (see README). This example is consumer-only because the
// outbound (publish) half of the binding needs the Pub/Sub SDK, a dependency decision this
// repo hasn't taken - see the gcppubsub package doc.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/gcppubsub"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

// greetHandler logs the greeting rather than returning it to anyone - a push delivery is
// fire-and-forget, so the log line (visible in Cloud Logging) is the observable effect, and
// the result's status decides ack (204) vs nack-and-redeliver (500).
func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	greeting := "Hello, " + req.Name + "!"
	log.Printf("greeted: %s", greeting)
	return benzene.Ok(greetResponse{Greeting: greeting})
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

// newHandler mounts the push endpoint at /pubsub - the path the subscription's
// --push-endpoint points at (see README). Everything else 404s, so a scanner hitting the
// service root doesn't reach the dispatch pipeline.
func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/pubsub", gcppubsub.Handler(builder))
	return mux
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
	log.Printf("gcp-pubsub-helloworld listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
