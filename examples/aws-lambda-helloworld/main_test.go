package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// These tests boot the real app from its composition root (newApp) and push native events in the
// front door: an API Gateway v2 / Function URL request via benzenetest.SendAPIGateway, and the
// raw wire envelope via benzenetest.SendEnvelope - the same harness shape every transport uses.

func newTestHost() *benzenetest.Host {
	return benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))
}

func TestNewHandler_FunctionURLEventReturnsGreeting(t *testing.T) {
	resp := benzenetest.SendAPIGateway(t, newTestHost(), http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(resp.Body) error = %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestNewHandler_FunctionURLMissingNameIsBadRequest(t *testing.T) {
	resp := benzenetest.SendAPIGateway(t, newTestHost(), http.MethodPost, "/greet", greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("statusCode = %d, want 400", resp.StatusCode)
	}
}

func TestNewHandler_EnvelopeEventRoundTrip(t *testing.T) {
	resp := benzenetest.SendEnvelope(t, newTestHost(), benzene.NewTopic("greet"), greetRequest{Name: "Envelope"}, nil)

	if resp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("StatusCode = %q, want %q; body = %s", resp.StatusCode, benzene.StatusOk, resp.Body)
	}
}

func TestNewHandler_MalformedEventIsError(t *testing.T) {
	// This exercises newHandler's own HTTP-vs-envelope dispatch fallback (app-specific glue, not a
	// Benzene feature), so it drives the combined handler directly rather than a single transport.
	handler := newHandler(newApp().Run())

	// Neither a valid HTTP-v2 event (no requestContext) nor a valid envelope - falls through to
	// EnvelopeHandler, which reports it as an error.
	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}
