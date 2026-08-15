package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
	"github.com/daniellepelley/benzene-go/wire"
)

func newTestHandler() (*meshd.Collector, func(context.Context, json.RawMessage) (json.RawMessage, error)) {
	collector := meshd.New(meshd.Options{})
	return collector, newMeshHandler(collector)
}

func TestMeshHandler_DirectInvokeRegistersService(t *testing.T) {
	_, handler := newTestHandler()
	event := benzenetest.NewEnvelopeEvent(t, benzene.NewTopic(mesh.TopicRegister), mesh.Descriptor{Service: "orders", Runtime: "go"}, nil)

	raw, err := handler(context.Background(), event)
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	resp, err := wire.UnmarshalResponse(raw)
	if err != nil {
		t.Fatalf("unmarshal envelope response: %v; raw = %s", err, raw)
	}
	if benzene.Status(resp.StatusCode).IsFailure() {
		t.Errorf("register StatusCode = %q, want a success status", resp.StatusCode)
	}
}

func TestMeshHandler_Discovered_CountsRegisteredServices(t *testing.T) {
	_, handler := newTestHandler()
	ctx := context.Background()

	for _, name := range []string{"orders", "payments", "shipping", "inventory", "notifications", "analytics"} {
		event := benzenetest.NewEnvelopeEvent(t, benzene.NewTopic(mesh.TopicRegister), mesh.Descriptor{Service: name, Runtime: "go"}, nil)
		if _, err := handler(ctx, event); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	raw, err := handler(ctx, benzenetest.NewAPIGatewayEvent(t, http.MethodGet, "/mesh/discovered", nil, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		StatusCode int    `json:"statusCode"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal HTTP response: %v; raw = %s", err, raw)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var got discoveredResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("unmarshal discovered body: %v; body = %s", err, resp.Body)
	}
	if got.Discovered != 6 {
		t.Errorf("Discovered = %d, want 6", got.Discovered)
	}
}

func TestMeshHandler_Discovered_EmptyFleet(t *testing.T) {
	_, handler := newTestHandler()
	raw, err := handler(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodGet, "/mesh/discovered", nil, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		Body string `json:"body"`
	}
	_ = json.Unmarshal(raw, &resp)
	var got discoveredResponse
	_ = json.Unmarshal([]byte(resp.Body), &got)
	if got.Discovered != 0 {
		t.Errorf("Discovered = %d, want 0 (nothing has registered yet)", got.Discovered)
	}
}

func TestMeshHandler_FleetUI_ServesTheMeshView(t *testing.T) {
	_, handler := newTestHandler()
	raw, err := handler(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodGet, meshd.ViewPath, nil, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		StatusCode int    `json:"statusCode"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Body, "Benzene Mesh") {
		t.Errorf("GET %s statusCode=%d, want 200 with the Mesh View page", meshd.ViewPath, resp.StatusCode)
	}
}

func TestMeshHandler_FleetUI_ServedAtRootToo(t *testing.T) {
	_, handler := newTestHandler()
	raw, err := handler(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodGet, "/", nil, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		StatusCode int    `json:"statusCode"`
		Body       string `json:"body"`
	}
	_ = json.Unmarshal(raw, &resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Body, "Benzene Mesh") {
		t.Errorf("GET / statusCode=%d, want 200 with the Mesh View page", resp.StatusCode)
	}
}

func TestMeshHandler_HTTPPostEnvelope_DispatchesFleetQuery(t *testing.T) {
	_, handler := newTestHandler()
	ctx := context.Background()

	registerEvent := benzenetest.NewEnvelopeEvent(t, benzene.NewTopic(mesh.TopicRegister), mesh.Descriptor{Service: "orders", Runtime: "go"}, nil)
	if _, err := handler(ctx, registerEvent); err != nil {
		t.Fatalf("register: %v", err)
	}

	envReq, err := wire.MarshalRequest(wire.Request{Topic: mesh.TopicQueryFleet, Headers: map[string]string{}, Body: "{}"})
	if err != nil {
		t.Fatalf("MarshalRequest: %v", err)
	}
	httpEvt := benzenetest.NewAPIGatewayEvent(t, http.MethodPost, httpbinding.EnvelopePath, json.RawMessage(envReq), nil)

	raw, err := handler(ctx, httpEvt)
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var outer struct {
		StatusCode int    `json:"statusCode"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("unmarshal outer HTTP response: %v; raw = %s", err, raw)
	}
	if outer.StatusCode != http.StatusOK {
		t.Fatalf("outer HTTP statusCode = %d, want 200 (the real outcome travels in the envelope)", outer.StatusCode)
	}
	envResp, err := wire.UnmarshalResponse([]byte(outer.Body))
	if err != nil {
		t.Fatalf("unmarshal envelope response: %v; body = %s", err, outer.Body)
	}
	var fleet meshd.FleetView
	if err := json.Unmarshal([]byte(envResp.Body), &fleet); err != nil {
		t.Fatalf("unmarshal fleet: %v; body = %s", err, envResp.Body)
	}
	if len(fleet.Services) != 1 || fleet.Services[0].Service != "orders" {
		t.Errorf("fleet.Services = %+v, want just [orders]", fleet.Services)
	}
}

func TestMeshHandler_UnknownHTTPRouteIs404(t *testing.T) {
	_, handler := newTestHandler()
	raw, err := handler(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodGet, "/no-such-route", nil, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct{ StatusCode int }
	_ = json.Unmarshal(raw, &resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("statusCode = %d, want 404", resp.StatusCode)
	}
}

func TestMeshHandler_MalformedEnvelopeOverHTTPIsBadRequest(t *testing.T) {
	_, handler := newTestHandler()
	event := benzenetest.NewAPIGatewayEvent(t, http.MethodPost, httpbinding.EnvelopePath, json.RawMessage(`not json`), nil)
	raw, err := handler(context.Background(), event)
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct{ StatusCode int }
	_ = json.Unmarshal(raw, &resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("statusCode = %d, want 400", resp.StatusCode)
	}
}
