package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

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
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	first := postGreet(t, server, "World")
	if first.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", first.Greeting, "Hello, World!")
	}
	if first.Count != 1 {
		t.Errorf("Count = %d, want 1", first.Count)
	}

	second := postGreet(t, server, "Go")
	if second.Count != 2 {
		t.Errorf("Count = %d, want 2 - the counter is a singleton shared across requests", second.Count)
	}
}

func TestGreetEndpoint_MissingNameIsBadRequest(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Post(server.URL+"/greet", "application/json", strings.NewReader(`{"name":""}`))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHealthEndpoint_ReturnsHealthy(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		IsHealthy    bool `json:"isHealthy"`
		HealthChecks map[string]struct {
			Status string `json:"status"`
		} `json:"healthChecks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("json.Decode() error = %v", err)
	}
	if !body.IsHealthy {
		t.Error("IsHealthy = false, want true")
	}
	if body.HealthChecks["memory"].Status != "ok" {
		t.Errorf(`HealthChecks["memory"].Status = %q, want "ok"`, body.HealthChecks["memory"].Status)
	}
}

func TestInvokeEndpoint_EnvelopeRoundTrip(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	reqBody, err := wire.MarshalRequest(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"Envelope"}`})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}
	resp, err := http.Post(server.URL+"/invoke", "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("outer HTTP status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var envResp wire.Response
	if err := json.NewDecoder(resp.Body).Decode(&envResp); err != nil {
		t.Fatalf("json.Decode() error = %v", err)
	}
	if envResp.StatusCode != "Ok" {
		t.Errorf("envelope statusCode = %q, want %q", envResp.StatusCode, "Ok")
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Get(server.URL + "/no-such-route")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer resp.Body.Close()

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

func postGreet(t *testing.T, server *httptest.Server, name string) greetResponse {
	t.Helper()
	body, err := json.Marshal(greetRequest{Name: name})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	resp, err := http.Post(server.URL+"/greet", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var greeting greetResponse
	if err := json.NewDecoder(resp.Body).Decode(&greeting); err != nil {
		t.Fatalf("json.Decode() error = %v", err)
	}
	return greeting
}
