package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// spyGreeter records the name it was asked to greet, so a test can prove the demo handler
// actually ran with the routed message - not merely that a 200 came back. Swapping it in via
// WithServices exercises the same DI seam a real test uses to replace an external dependency
// with a fake.
type spyGreeter struct{ gotName string }

func (s *spyGreeter) Greet(name string) string {
	s.gotName = name
	return "Hi there, " + name
}

func newTestHost(opts ...benzenetest.Option) *benzenetest.Host {
	return benzenetest.NewHost(newApp(), append([]benzenetest.Option{benzenetest.WithRoutes(routes()...)}, opts...)...)
}

// This boots the SAME app main() runs (both ConfigureServices and Configure) and pushes a
// native API Gateway HTTP event through the whole Benzene pipeline; only the transport trigger
// is simulated. The spy proves the routed message reached the handler.
func TestGreet_APIGatewayRequestRunsHandler(t *testing.T) {
	spy := &spyGreeter{}
	host := newTestHost(benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
		benzene.AddSingleton(b.Container, greeterKey, func(_ *benzene.Scope) Greeter { return spy })
	}))

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	if spy.gotName != "World" {
		t.Errorf("greeter called with %q, want %q - handler didn't run with the routed message", spy.gotName, "World")
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(resp.Body) error = %v", err)
	}
	if greeting.Greeting != "Hi there, World" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hi there, World")
	}
}

func TestGreet_MissingNameIsBadRequest(t *testing.T) {
	resp := benzenetest.SendAPIGateway(t, newTestHost(), http.MethodPost, "/greet", greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("statusCode = %d, want 400", resp.StatusCode)
	}
}

// The same app also answers a raw wire-envelope invoke (a Lambda-to-Lambda call with no route
// table), through the envelope front door.
func TestGreet_EnvelopeInvokeRoundTrip(t *testing.T) {
	resp := benzenetest.SendEnvelope(t, newTestHost(), benzene.NewTopic("greet"), greetRequest{Name: "Envelope"}, nil)

	if resp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("StatusCode = %q, want %q; body = %s", resp.StatusCode, benzene.StatusOk, resp.Body)
	}
}

// newHandler's own HTTP-vs-envelope dispatch is app glue, not a Benzene feature, so this drives
// the combined handler directly: a malformed event is neither shape and falls through to the
// envelope handler, which reports it as an error.
func TestNewHandler_MalformedEventIsError(t *testing.T) {
	handler := newHandler(newApp().Run())
	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}
