// Command http-helloworld hosts the greet handler on a standalone net/http server via the
// httpbinding package - the Go counterpart of the .NET repo's examples/Asp (hosting a Benzene
// service on the framework's own web server). Where examples/helloworld shows the binding's full
// surface (native routes + the wire envelope + health), this example is the focused "just host it
// on net/http" story: the greet route on a real *http.Server, wrapped in an ordinary net/http
// middleware, with graceful shutdown - the plumbing a real HTTP service needs around the binding.
//
// httpbinding is net/http-native and dependency-free, so this example lives in the root module (no
// third-party dependency, like examples/helloworld and examples/gcp-cloudrun-helloworld) rather
// than its own module.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// Greeter is the "port": greetHandler depends on this interface, not on how a greeting is produced.
type Greeter interface {
	Greet(name string) string
}

// politeGreeter is the adapter used by this example.
type politeGreeter struct{}

func (politeGreeter) Greet(name string) string { return "Hello, " + name + "!" }

// greeterKey is the typed DI key for the Greeter dependency - a struct key can't collide with
// another package's key, unlike a bare string (see helloworld's main.go for the rationale).
type greeterKey struct{}

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

// greetHandler resolves its Greeter via ScopeFromContext (see helloworld's main.go) and answers
// with the greeting.
func greetHandler(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[greetResponse]("no DI scope on context")
	}
	greeter := benzene.GetService[Greeter](scope, greeterKey{})
	return benzene.Ok(greetResponse{Greeting: greeter.Greet(req.Name)})
}

// newApp is the composition root: the three-phase benzene.App both main() and the test boot from.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.AddSingleton(container, greeterKey{}, func(_ *benzene.Scope) Greeter { return politeGreeter{} })
			if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

func routes() []httpbinding.Route {
	return []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
	}
}

// requestCounter is an ordinary net/http middleware wrapping the Benzene binding - proof that the
// binding is a plain http.Handler that composes with any net/http middleware you already use
// (logging, auth, metrics). It counts requests and logs one line each.
type requestCounter struct {
	next  http.Handler
	count atomic.Int64
}

func (m *requestCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := m.count.Add(1)
	log.Printf("#%d %s %s", n, r.Method, r.URL.Path)
	m.next.ServeHTTP(w, r)
}

// newHandler builds the net/http handler: httpbinding.Handler (the greet route) wrapped in the
// request-counting middleware.
func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return &requestCounter{next: httpbinding.Handler(builder, routes())}
}

func portFromEnv() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func main() {
	builder := newApp().Run()
	server := &http.Server{
		Addr:              ":" + portFromEnv(),
		Handler:           newHandler(builder),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown: serve until SIGINT/SIGTERM, then drain in-flight requests.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("http-helloworld listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
	log.Print("http-helloworld stopped")
}
