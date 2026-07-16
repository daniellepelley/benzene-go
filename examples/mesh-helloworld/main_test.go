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

	greeter := newService("greeter", meshdEndpoint, true, func(registry *benzene.Registry) {
		if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
			t.Fatalf("register greet: %v", err)
		}
	}, []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}})
	greeterServer := httptest.NewServer(greeter.handler)
	defer greeterServer.Close()

	frontdoor := newService("frontdoor", meshdEndpoint, true, func(registry *benzene.Registry) {
		greeterClient := httpclient.NewClient(greeterServer.URL + "/invoke")
		if err := benzene.Register(registry, benzene.NewTopic("welcome"), welcomeHandler(greeterClient)); err != nil {
			t.Fatalf("register welcome: %v", err)
		}
	}, []httpbinding.Route{{Method: http.MethodPost, Path: "/welcome", Topic: benzene.NewTopic("welcome")}})
	frontdoorServer := httptest.NewServer(frontdoor.handler)
	defer frontdoorServer.Close()

	// legacy-portal: trace feed only - no descriptor endpoint, no announce, no heartbeat.
	legacy := newService("legacy-portal", meshdEndpoint, false, func(registry *benzene.Registry) {
		greeterClient := httpclient.NewClient(greeterServer.URL + "/invoke")
		if err := benzene.Register(registry, benzene.NewTopic("legacy:relay"), welcomeHandler(greeterClient)); err != nil {
			t.Fatalf("register legacy:relay: %v", err)
		}
	}, []httpbinding.Route{{Method: http.MethodPost, Path: "/relay", Topic: benzene.NewTopic("legacy:relay")}})
	legacyServer := httptest.NewServer(legacy.handler)
	defer legacyServer.Close()

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

	// And one through the reduced service.
	relayResponse, err := http.Post(legacyServer.URL+"/relay", "application/json", strings.NewReader(`{"name":"Legacy"}`))
	if err != nil {
		t.Fatalf("POST /relay: %v", err)
	}
	relayBody, _ := io.ReadAll(relayResponse.Body)
	relayResponse.Body.Close()
	if relayResponse.StatusCode != http.StatusOK {
		t.Fatalf("POST /relay = %d %s - a reduced service must serve like any other", relayResponse.StatusCode, relayBody)
	}

	// The reserved mesh topic serves the descriptor - schemas and contract hash included.
	descResult := httpclient.NewClient(greeterServer.URL+"/invoke").Send(ctx, benzene.NewTopic(mesh.TopicID), nil, nil)
	descriptor, err := httpclient.Unmarshal[mesh.Descriptor](descResult)
	if err != nil || descriptor.Payload == nil {
		t.Fatalf("mesh topic: %v %v", descResult.Status, err)
	}
	if descriptor.Payload.Service != "greeter" || len(descriptor.Payload.Topics) != 1 || descriptor.Payload.Topics[0].RequestSchema == nil {
		t.Errorf("descriptor = %+v, want greeter's derived contract with schemas", descriptor.Payload)
	}
	if !strings.HasPrefix(descriptor.Payload.DescriptorHash, "sha256:") {
		t.Errorf("DescriptorHash = %q, want sha256:…", descriptor.Payload.DescriptorHash)
	}

	// Flush every trace feed, then read the fleet back from the collector.
	greeter.exporter.Close()
	frontdoor.exporter.Close()
	legacy.exporter.Close()

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

	// The reduced service is on the view too: anonymous-but-live, visibly reduced.
	legacySummary, ok := services["legacy-portal"]
	if !ok {
		t.Fatalf("fleet is missing legacy-portal: %+v", fleet.Payload.Services)
	}
	if len(legacySummary.MissingFeeds) != 2 || legacySummary.MissingFeeds[0] != "descriptor" || legacySummary.MissingFeeds[1] != "health" {
		t.Errorf("legacy-portal missingFeeds = %v, want [descriptor health]", legacySummary.MissingFeeds)
	}
	if legacySummary.Invocations == 0 {
		t.Errorf("legacy-portal invocations = 0, want its traced traffic counted despite the reduced feeds")
	}

	// Consumer edges were derived from trace parentage, not declared anywhere - including
	// the reduced service's edge.
	topicResult := meshdClient.Send(ctx, benzene.NewTopic(mesh.TopicQueryTopic), nil, []byte(`{"topic":"greet"}`))
	greet, err := httpclient.Unmarshal[meshd.TopicSummary](topicResult)
	if err != nil || greet.Payload == nil {
		t.Fatalf("topic query: %v %v", topicResult.Status, err)
	}
	if len(greet.Payload.Providers) != 1 || greet.Payload.Providers[0] != "greeter" {
		t.Errorf("greet providers = %v, want [greeter]", greet.Payload.Providers)
	}
	if len(greet.Payload.Consumers) != 2 || greet.Payload.Consumers[0] != "frontdoor" || greet.Payload.Consumers[1] != "legacy-portal" {
		t.Errorf("greet consumers = %v, want [frontdoor legacy-portal] derived from propagated traceparents", greet.Payload.Consumers)
	}

	// Each cross-service call joined into one flow of two events. (The descriptor query
	// above was itself traced - the trace middleware sees every invocation, including
	// reserved-topic interceptions - so it appears as a third, single-event flow.)
	var crossServiceFlows []meshd.TraceSummary
	for _, flow := range fleet.Payload.Traces {
		if flow.Events == 2 {
			crossServiceFlows = append(crossServiceFlows, flow)
		}
	}
	if len(crossServiceFlows) != 2 {
		t.Fatalf("Traces = %+v, want the welcome flow and the relay flow among them", fleet.Payload.Traces)
	}
	for _, flow := range crossServiceFlows {
		callers := map[string]bool{}
		for _, svc := range flow.Services {
			callers[svc] = true
		}
		if len(flow.Services) != 2 || !callers["greeter"] || (!callers["frontdoor"] && !callers["legacy-portal"]) {
			t.Errorf("flow = %+v, want two events across caller+greeter", flow)
		}
	}

	// Drill into one flow the way the view's flow explorer would.
	traceResult := meshdClient.Send(ctx, benzene.NewTopic(mesh.TopicQueryTrace), nil, []byte(`{"traceId":"`+crossServiceFlows[0].TraceID+`"}`))
	flow, err := httpclient.Unmarshal[meshd.TraceView](traceResult)
	if err != nil || flow.Payload == nil {
		t.Fatalf("trace query: %v %v", traceResult.Status, err)
	}
	if len(flow.Payload.Events) != 2 || flow.Payload.Events[1].ParentSpanID != flow.Payload.Events[0].SpanID {
		t.Errorf("flow events = %+v, want the greet span parented on the caller span", flow.Payload.Events)
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
	greeter := newService("greeter", unreachable, true, func(registry *benzene.Registry) {
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
