package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// These tests boot the real app from its composition root (newApp) and push native events in the
// front door with the benzenetest harness - the same shape every transport uses. The native-HTTP
// front door is benzenetest.SendHTTP; the raw wire-envelope front door is benzenetest.SendEnvelope.
// To test these handlers on a cloud host instead, only the Send* call would change (SendAPIGateway
// on AWS, SendPubSub on GCP, ...). Lines that create the host and assert stay identical.

func TestGreetHandler_NoScopeOnContextIsUnexpectedError(t *testing.T) {
	// A direct unit-level call bypassing the HTTP wiring, which always attaches a scope
	// (RouterMiddleware/envelope.Dispatch) - this defends against greetHandler ever being
	// invoked outside that wiring, e.g. from a future test or reused-elsewhere call site.
	result := greetHandler(context.Background(), greetRequest{Name: "World"})

	if result.Status != benzene.StatusUnexpectedError {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusUnexpectedError)
	}
}

func TestGreetEndpoint_ReturnsGreetingAndIncrementsCount(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

	first := postGreet(t, host, "World")
	if first.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", first.Greeting, "Hello, World!")
	}
	if first.Count != 1 {
		t.Errorf("Count = %d, want 1", first.Count)
	}

	second := postGreet(t, host, "Go")
	if second.Count != 2 {
		t.Errorf("Count = %d, want 2 - the counter is a singleton shared across requests", second.Count)
	}
}

func TestGreetEndpoint_MissingNameIsBadRequest(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHealthEndpoint_ReturnsHealthy(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodGet, httpbinding.HealthPath, nil, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
	}
	var body struct {
		IsHealthy    bool `json:"isHealthy"`
		HealthChecks map[string]struct {
			Status string `json:"status"`
		} `json:"healthChecks"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, resp.Body)
	}
	if !body.IsHealthy {
		t.Error("IsHealthy = false, want true")
	}
	if body.HealthChecks["memory"].Status != "ok" {
		t.Errorf(`HealthChecks["memory"].Status = %q, want "ok"`, body.HealthChecks["memory"].Status)
	}
}

func TestInvokeEndpoint_EnvelopeRoundTrip(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := benzenetest.SendEnvelope(t, host, benzene.NewTopic("greet"), greetRequest{Name: "Envelope"}, nil)

	if resp.StatusCode != string(benzene.StatusOk) {
		t.Fatalf("envelope statusCode = %q, want %q; body = %s", resp.StatusCode, benzene.StatusOk, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, resp.Body)
	}
	if greeting.Greeting != "Hello, Envelope!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, Envelope!")
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodGet, "/no-such-route", nil, nil)

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestPortFromEnv_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("PORT", "")
	if got := portFromEnv(); got != "8080" {
		t.Errorf("portFromEnv() = %q, want %q", got, "8080")
	}
}

func TestPortFromEnv_UsesEnvWhenSet(t *testing.T) {
	t.Setenv("PORT", "9090")
	if got := portFromEnv(); got != "9090" {
		t.Errorf("portFromEnv() = %q, want %q", got, "9090")
	}
}

// postGreet pushes a greet request through the native-HTTP front door and decodes the native
// response body, so the increment assertions read against the real mapped HTTP response.
func postGreet(t *testing.T, host *benzenetest.Host, name string) greetResponse {
	t.Helper()
	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: name}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, resp.Body)
	}
	return greeting
}
