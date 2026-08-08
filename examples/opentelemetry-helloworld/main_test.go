package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/diagnostics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// These tests boot the real app from its composition root (newApp) via benzenetest and push a native
// HTTP request in the front door - the same shape helloworld's tests use - while capturing the
// OpenTelemetry spans the diagnostics middleware and the Greeter adapter emit. Instead of the stdout
// exporter main() installs, the test wires an in-memory exporter: it drives the same pipeline over
// the real HTTP binding and then asserts on the spans that pipeline produced, so no collector is
// needed.

// newHostAndExporter boots the app with an in-memory-exporting tracer provider, wired into both the
// diagnostics middleware (via WithTracerProvider) and the global provider the Greeter adapter's child
// span reads, so every span this example emits lands in the returned exporter.
func newHostAndExporter(t *testing.T) (*benzenetest.Host, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	host := benzenetest.NewHost(newApp(diagnostics.WithTracerProvider(tp)), benzenetest.WithRoutes(routes()...))
	return host, exporter
}

func spanNamed(spans tracetest.SpanStubs, name string) (tracetest.SpanStub, bool) {
	for _, s := range spans {
		if s.Name == name {
			return s, true
		}
	}
	return tracetest.SpanStub{}, false
}

func attrValue(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func TestGreet_EmitsServerSpanAndNestedGreeterSpan(t *testing.T) {
	host, exporter := newHostAndExporter(t)

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, resp.Body)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}

	spans := exporter.GetSpans()

	server, ok := spanNamed(spans, "greet")
	if !ok {
		t.Fatalf("no server span named %q; got %d spans", "greet", len(spans))
	}
	if got, ok := attrValue(server.Attributes, "benzene.status"); !ok || got != "ok" {
		t.Errorf(`server span attribute "benzene.status" = %q (present=%v), want "ok"`, got, ok)
	}

	child, ok := spanNamed(spans, "Greeter.Greet")
	if !ok {
		t.Fatalf("no child span named %q; got %d spans", "Greeter.Greet", len(spans))
	}
	if got, ok := attrValue(child.Attributes, "greet.name"); !ok || got != "World" {
		t.Errorf(`child span attribute "greet.name" = %q (present=%v), want "World"`, got, ok)
	}
	// The Greeter span must nest under the server span - the whole point of threading the
	// span-carrying context through the handler into the adapter.
	if child.Parent.SpanID() != server.SpanContext.SpanID() {
		t.Errorf("child span parent = %v, want the server span's id %v", child.Parent.SpanID(), server.SpanContext.SpanID())
	}
}

func TestGreet_FailureMarksServerSpanAsError(t *testing.T) {
	host, exporter := newHostAndExporter(t)

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: ""}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	server, ok := spanNamed(exporter.GetSpans(), "greet")
	if !ok {
		t.Fatal("no server span named \"greet\"")
	}
	if got, _ := attrValue(server.Attributes, "benzene.status"); got != "bad-request" {
		t.Errorf(`server span "benzene.status" = %q, want "bad-request"`, got)
	}
}
