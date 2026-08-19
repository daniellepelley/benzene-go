package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// These tests boot the real app from its composition root (newApp) and push native net/http
// requests in the front door with benzenetest.SendHTTP - the same shape helloworld's tests use.

func TestGreetEndpoint_ReturnsGreeting(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

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
}

func TestGreetEndpoint_MissingNameIsBadRequest(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: ""}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestGreetEndpoint_UsesOverriddenGreeter(t *testing.T) {
	host := benzenetest.NewHost(newApp(),
		benzenetest.WithRoutes(routes()...),
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			benzene.AddSingleton(b.Container, greeterKey{}, func(_ *benzene.Scope) Greeter { return fixedGreeter{} })
		}))

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, resp.Body)
	}
	if greeting.Greeting != "Greetings, program!" {
		t.Errorf("Greeting = %q, want the overridden adapter's %q", greeting.Greeting, "Greetings, program!")
	}
}

func TestMiddleware_WrapsTheBindingAndCounts(t *testing.T) {
	// The request-counting net/http middleware must wrap the binding and pass requests through -
	// proof that httpbinding.Handler is an ordinary http.Handler that composes with net/http
	// middleware.
	builder := newApp().Run()
	handler, ok := newHandler(builder).(*requestCounter)
	if !ok {
		t.Fatalf("newHandler returned %T, want *requestCounter", newHandler(builder))
	}

	req := benzenetest.NewHTTPRequest(t, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := handler.count.Load(); got != 1 {
		t.Errorf("count = %d, want 1", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
}

// fixedGreeter is a test spy adapter proving the port is swappable.
type fixedGreeter struct{}

func (fixedGreeter) Greet(string) string { return "Greetings, program!" }
