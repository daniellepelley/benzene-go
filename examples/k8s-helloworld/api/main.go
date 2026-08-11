// Command api is the HTTP leg of this example: a plain net/http server exposing POST /greet,
// dispatching into the shared greeting.Handler - the same handler sqsworker and kafkaworker use.
// There is nothing Kubernetes-specific here; it's hosted exactly like
// examples/gcp-cloudrun-helloworld, just pointed at the shared handler package instead of one it
// defines itself.
package main

import (
	"log"
	"net/http"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"

	"github.com/daniellepelley/benzene-go/examples/k8s-helloworld/greeting"
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)); err != nil {
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

// portFromEnv reads $PORT (the readinessProbe and Service target this) - defaulting to 8080 for
// local runs.
func portFromEnv() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func main() {
	handler := newHandler(newApp())
	port := portFromEnv()
	log.Printf("k8s-helloworld api listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
