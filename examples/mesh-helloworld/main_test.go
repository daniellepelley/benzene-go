package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
)

// TestMeshHelloworldEndToEnd runs the whole story over real HTTP: collector up, both
// services up and announced, one cross-service call - then asserts the collector derived
// the fleet, the flow, and the consumer edge from what the services emitted.
func TestMeshHelloworldEndToEnd(t *testing.T) {
	meshdServer := httptest.NewServer(newMeshd())
	defer meshdServer.Close()
	meshdEndpoint := meshdServer.URL + "/invoke"

	greeter := newService("greeter", meshdEndpoint, func(registry *benzene.Registry) {
		if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
			t.Fatalf("register greet: %v", err)
		}
	}, []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}})
	greeterServer := httptest.NewServer(greeter.handler)
	defer greeterServer.Close()

	frontdoor := newService("frontdoor", meshdEndpoint, func(registry *benzene.Registry) {
		greeterClient := httpclient.NewClient(greeterServer.URL + "/invoke")
		if err := benzene.Register(registry, benzene.NewTopic("welcome"), welcomeHandler(greeterClient)); err != nil {
			t.Fatalf("register welcome: %v", err)
		}
	}, []httpbinding.Route{{Method: http.MethodPost, Path: "/welcome", Topic: benzene.NewTopic("welcome")}})
	frontdoorServer := httptest.NewServer(frontdoor.handler)
	defer frontdoorServer.Close()

	ctx := context.Background()
	greeter.announce(ctx)
	frontdoor.announce(ctx)
	greeter.heartbeat(ctx)
	frontdoor.heartbeat(ctx)

	// One cross-service call through frontdoor's native route.
	response, err := http.Post(frontdoorServer.URL+"/welcome", "application/json", strings.NewReader(`{"name":"Mesh"}`))
	if err != nil {
		t.Fatalf("POST /welcome: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /welcome = %d %s", response.StatusCode, body)
	}
	var welcome welcomeResponse
	if err := json.Unmarshal(body, &welcome); err != nil {
		t.Fatalf("unmarshal welcome response %q: %v", body, err)
	}
	if welcome.Message != "frontdoor relays: Hello, Mesh!" {
		t.Fatalf("Message = %q, want the relayed greeting", welcome.Message)
	}

	// Flush both trace feeds, then read the fleet back from the collector.
	greeter.exporter.Close()
	frontdoor.exporter.Close()

	meshdClient := httpclient.NewClient(meshdEndpoint)
	result := meshdClient.Send(ctx, benzene.NewTopic(mesh.TopicQueryFleet), nil, []byte(`{}`))
	if !result.IsSuccessful() {
		t.Fatalf("fleet query = %v %v", result.Status, result.Errors)
	}
	fleet, err := httpclient.Unmarshal[meshd.FleetView](result)
	if err != nil || fleet.Payload == nil {
		t.Fatalf("unmarshal fleet: %v", err)
	}

	services := map[string]meshd.ServiceSummary{}
	for _, svc := range fleet.Payload.Services {
		services[svc.Service] = svc
	}
	for _, name := range []string{"greeter", "frontdoor"} {
		svc, ok := services[name]
		if !ok {
			t.Fatalf("fleet is missing %q: %+v", name, fleet.Payload.Services)
		}
		if svc.Health != "healthy" {
			t.Errorf("%s health = %q, want healthy (heartbeat was sent)", name, svc.Health)
		}
		if svc.Invocations == 0 {
			t.Errorf("%s invocations = 0, want traced traffic", name)
		}
		if len(svc.MissingFeeds) != 0 {
			t.Errorf("%s missingFeeds = %v, want none (all three feeds provisioned)", name, svc.MissingFeeds)
		}
	}

	// The consumer edge was derived from trace parentage, not declared anywhere.
	topicResult := meshdClient.Send(ctx, benzene.NewTopic(mesh.TopicQueryTopic), nil, []byte(`{"topic":"greet"}`))
	greet, err := httpclient.Unmarshal[meshd.TopicSummary](topicResult)
	if err != nil || greet.Payload == nil {
		t.Fatalf("topic query: %v %v", topicResult.Status, err)
	}
	if len(greet.Payload.Providers) != 1 || greet.Payload.Providers[0] != "greeter" {
		t.Errorf("greet providers = %v, want [greeter]", greet.Payload.Providers)
	}
	if len(greet.Payload.Consumers) != 1 || greet.Payload.Consumers[0] != "frontdoor" {
		t.Errorf("greet consumers = %v, want [frontdoor] derived from the propagated traceparent", greet.Payload.Consumers)
	}

	// Both events joined into one flow.
	if len(fleet.Payload.Traces) != 1 || fleet.Payload.Traces[0].Events != 2 {
		t.Fatalf("Traces = %+v, want one flow with both services' events", fleet.Payload.Traces)
	}
	if got := fleet.Payload.Traces[0].Services; len(got) != 2 || got[0] != "frontdoor" || got[1] != "greeter" {
		t.Errorf("flow services = %v, want frontdoor and greeter", got)
	}

	// And the Mesh View is served at the collector's root.
	page, err := http.Get(meshdServer.URL + "/")
	if err != nil {
		t.Fatalf("GET view: %v", err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(string(pageBody), "Benzene Mesh") {
		t.Errorf("view = %d, want the Mesh View page", page.StatusCode)
	}
}

// TestReducedMeshStillServes proves the degradation rule end to end: no collector
// reachable, no descriptor endpoint provisioned - the service must behave exactly as an
// unmeshed one.
func TestReducedMeshStillServes(t *testing.T) {
	unreachable := "http://127.0.0.1:1/invoke" // nothing listens there
	greeter := newService("greeter", unreachable, func(registry *benzene.Registry) {
		if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
			t.Fatalf("register greet: %v", err)
		}
	}, []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}})
	server := httptest.NewServer(greeter.handler)
	defer server.Close()
	defer greeter.exporter.Close()

	greeter.announce(context.Background())  // logs, must not fail
	greeter.heartbeat(context.Background()) // logs, must not fail

	response, err := http.Post(server.URL+"/greet", "application/json", strings.NewReader(`{"name":"Mesh"}`))
	if err != nil {
		t.Fatalf("POST /greet: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST /greet = %d %s - a dead collector must never affect the service", response.StatusCode, body)
	}
}
