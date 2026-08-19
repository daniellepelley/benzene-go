// Command opentelemetry-helloworld is the OpenTelemetry analogue of examples/helloworld: the same
// greet handler behind a port interface, wrapped this time in the diagnostics package's tracing
// middleware so every invocation emits an OpenTelemetry span. It is the Go counterpart of the .NET
// repo's examples/OpenTelemetry.
//
// The diagnostics middleware depends on the OpenTelemetry API only; the SDK (exporter, sampler) is
// the application's to own. This example owns it here: main() installs an SDK TracerProvider with a
// stdout exporter, so running the service and hitting it prints each pipeline span to the console -
// a self-contained trace demo with no collector to stand up. Point the same wiring at an OTLP
// exporter (go.opentelemetry.io/otel/exporters/otlp/...) to ship to Tempo/Jaeger/Datadog instead;
// nothing about the middleware changes.
//
// The Greeter adapter starts its own child span, so the exported trace shows a "Greeter.Greet" span
// nested under the Benzene server span - the outbound/nested-work shape a real service's spans take.
package main

import (
	"context"
	"log"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/diagnostics"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Greeter is the "port": greetHandler depends on this interface, not on how a greeting is produced.
// Its Greet takes a context so the adapter can start a child span nested under the invocation's
// server span.
type Greeter interface {
	Greet(ctx context.Context, name string) string
}

// tracingGreeter is the adapter used by this example: it starts a "Greeter.Greet" child span (via
// the ambient OpenTelemetry tracer the SDK is registered under) before producing the greeting.
type tracingGreeter struct{}

func (tracingGreeter) Greet(ctx context.Context, name string) string {
	_, span := otel.Tracer(tracerName).Start(ctx, "Greeter.Greet")
	defer span.End()
	span.SetAttributes(attribute.String("greet.name", name))
	return "Hello, " + name + "!"
}

// tracerName identifies this example's own instrumentation (the child span) to the tracer provider.
const tracerName = "github.com/daniellepelley/benzene-go/examples/opentelemetry-helloworld"

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
// with the greeting. It receives the span-carrying context from the diagnostics middleware, which
// it passes to the Greeter so the child span nests correctly.
func greetHandler(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[greetResponse]("no DI scope on context")
	}
	greeter := benzene.GetService[Greeter](scope, greeterKey{})
	return benzene.Ok(greetResponse{Greeting: greeter.Greet(ctx, req.Name)})
}

// newApp is the composition root. diagnostics.Middleware is registered outermost (ahead of
// healthcheck/router) so it observes every invocation; opts lets a test inject an in-memory tracer
// provider, while main() leaves it empty and relies on the global provider it installs.
func newApp(opts ...diagnostics.Option) benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.AddSingleton(container, greeterKey{}, func(_ *benzene.Scope) Greeter { return tracingGreeter{} })
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			checks := []healthcheck.Check{
				healthcheck.NamedCheck("memory", func(context.Context) healthcheck.CheckResult {
					return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "memory"}
				}),
			}
			builder.UsePipeline(benzene.NewPipeline(
				diagnostics.Middleware(opts...),
				healthcheck.Middleware(checks),
				benzene.RouterMiddleware(builder.Registry),
			))
		},
	}
}

func routes() []httpbinding.Route {
	return []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	}
}

func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	mux.Handle("/", httpbinding.Handler(builder, routes()))
	return mux
}

// newTracerProvider builds the SDK TracerProvider main() installs: a stdout exporter (pretty-printed
// spans to the console) and an always-on sampler, so every request's trace is visible when you run
// the demo. Swap stdouttrace for an OTLP exporter to ship elsewhere.
func newTracerProvider() (*sdktrace.TracerProvider, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	), nil
}

func main() {
	tp, err := newTracerProvider()
	if err != nil {
		log.Fatalf("tracer provider: %v", err)
	}
	otel.SetTracerProvider(tp)
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("tracer shutdown: %v", err)
		}
	}()

	builder := newApp().Run()
	handler := newHandler(builder)
	addr := httpbinding.ListenAddr()

	log.Printf("opentelemetry-helloworld listening on %s (spans print to stdout)", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
