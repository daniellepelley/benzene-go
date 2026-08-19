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

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/gcppubsub"
	"github.com/daniellepelley/benzene-go/httpbinding"
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

// newApp is the composition root both main() and the tests boot from.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
		},
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

func main() {
	handler := newHandler(newApp().Run())
	addr := httpbinding.ListenAddr()
	log.Printf("gcp-pubsub-helloworld listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
