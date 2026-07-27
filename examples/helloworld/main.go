// Command helloworld is a minimal end-to-end Benzene service: one handler behind a port
// interface (the hexagonal-architecture shape this whole project is named for), a health
// check, and both of the httpbinding package's HTTP entry points, wired through the
// three-phase App lifecycle of core-concepts.md §7.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// GreetingCounter is the "port": the handler depends on this interface, not on how counting is
// actually implemented. Swapping inMemoryGreetingCounter for a Redis- or database-backed
// adapter would require no change to greetHandler or its registration.
type GreetingCounter interface {
	Increment() int
}

// inMemoryGreetingCounter is the adapter used by this example - process-local and therefore
// reset on every restart, which is fine for a hello-world demo.
type inMemoryGreetingCounter struct {
	mu sync.Mutex
	n  int
}

func (c *inMemoryGreetingCounter) Increment() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

const greetingCounterKey = "greeting-counter"

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
	Count    int    `json:"count"`
}

// greetHandler resolves its GreetingCounter dependency via ScopeFromContext rather than a
// parameter on the Handler signature - see scope.go's ContextWithScope doc comment for why.
// The counter is registered as a singleton, so every invocation's scope resolves to the same
// instance.
func greetHandler(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}

	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[greetResponse]("no DI scope on context")
	}
	counter := benzene.GetService[GreetingCounter](scope, greetingCounterKey)

	return benzene.Ok(greetResponse{
		Greeting: "Hello, " + req.Name + "!",
		Count:    counter.Increment(),
	})
}

// newApp is the composition root: the three-phase benzene.App (core-concepts.md §7) both main()
// and the tests boot from, so a test exercises exactly the wiring that ships. main() calls
// .Run() to build the pipeline; the tests hand the App to benzenetest.NewHost, which runs the
// same lifecycle with the WithServices test seam.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.AddSingleton(container, greetingCounterKey, func(_ *benzene.Scope) GreetingCounter {
				return &inMemoryGreetingCounter{}
			})
			if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			checks := []healthcheck.Check{
				healthcheck.CheckFunc{CheckName: "memory", Fn: func(context.Context) healthcheck.CheckResult {
					return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "memory"}
				}},
			}
			builder.UsePipeline(benzene.NewPipeline(
				healthcheck.Middleware(checks),
				benzene.RouterMiddleware(builder.Registry),
			))
		},
	}
}

// routes is the HTTP route table (path/method -> topic) the native httpbinding entry point
// mounts. It lives in one place so main() and the tests (via benzenetest.WithRoutes) agree on
// exactly the same routing.
func routes() []httpbinding.Route {
	return []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic("healthcheck")},
	}
}

// newHandler builds the HTTP entry point: /greet as a native REST-style route
// (httpbinding.Handler), plus the default service standard's well-known surfaces
// (docs/specification/design-principles.md §5 in the main repo) - health at
// httpbinding.HealthPath and the raw wire-contracts.md envelope
// (httpbinding.EnvelopeHandler) at httpbinding.EnvelopePath for service-to-service calls
// with no route table to agree on. The /benzene/ prefix marks them as framework
// infrastructure, not domain endpoints.
func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	mux.Handle("/", httpbinding.Handler(builder, routes()))
	return mux
}

func portFromEnv() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func main() {
	builder := newApp().Run()
	handler := newHandler(builder)
	port := portFromEnv()

	log.Printf("helloworld listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
