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
	"github.com/daniellepelley/benzene-go/examples/k8s-mesh-helloworld/domain"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
)

// newTestCollector starts a real meshd collector over HTTP, mirroring examples/mesh-helloworld's
// newMeshd() — used here as the fixture the three chained services report and register to.
func newTestCollector(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	collector := meshd.New(meshd.Options{})
	server := httptest.NewServer(httpbinding.EnvelopeHandler(collector.Builder()))
	t.Cleanup(server.Close)
	return server, server.URL
}

// TestChain_OrdersToPaymentsToShipping wires the three real domain services exactly as
// k8s/services.yaml does — orders -> payments -> shipping, all reporting to one collector — over
// httptest instead of Kubernetes DNS, and proves the whole chain, the mesh visibility, and the
// terminal service's lack of a downstream client all behave as documented.
func TestChain_OrdersToPaymentsToShipping(t *testing.T) {
	_, collectorURL := newTestCollector(t)

	// shipping first: it's terminal, no DOWNSTREAM_MSG_URL.
	shipping := newService("shipping", "", collectorURL)
	shippingServer := httptest.NewServer(shipping.handler)
	defer shippingServer.Close()
	defer shipping.exporter.Close()
	defer shipping.issueExporter.Close()
	if shipping.name != "shipping" {
		t.Fatalf("shipping.name = %q", shipping.name)
	}

	payments := newService("payments", shippingServer.URL+httpbinding.EnvelopePath, collectorURL)
	paymentsServer := httptest.NewServer(payments.handler)
	defer paymentsServer.Close()
	defer payments.exporter.Close()
	defer payments.issueExporter.Close()

	orders := newService("orders", paymentsServer.URL+httpbinding.EnvelopePath, collectorURL)
	ordersServer := httptest.NewServer(orders.handler)
	defer ordersServer.Close()
	defer orders.exporter.Close()
	defer orders.issueExporter.Close()

	ctx := context.Background()
	for _, svc := range []*service{shipping, payments, orders} {
		if !svc.announce(ctx) {
			t.Fatalf("%s: announce failed", svc.name)
		}
		svc.heartbeat(ctx)
	}

	// Drive the chain through orders' native route, exactly like the README's curl example.
	resp, err := http.Post(ordersServer.URL+"/orders", "application/json",
		strings.NewReader(`{"customerId":"cust-1","sku":"espresso","quantity":2}`))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /orders = %d %s", resp.StatusCode, body)
	}
	var order domain.OrderCreated
	if err := json.Unmarshal(body, &order); err != nil {
		t.Fatalf("unmarshal order %q: %v", body, err)
	}
	if order.Status != "created" {
		t.Errorf("order.Status = %q, want created", order.Status)
	}

	// Also drive shipping's envelope endpoint directly, addressing a topic it owns - the same
	// "hit any service's envelope endpoint directly" path the README documents.
	shipResult := httpclient.NewClient(shippingServer.URL+httpbinding.EnvelopePath).
		Send(ctx, benzene.NewTopic(domain.TopicShipmentBook), nil, []byte(`{"orderId":"o-9","address":"x","carrier":"y"}`))
	if !shipResult.IsSuccessful() {
		t.Fatalf("shipment:book direct call: %v %v", shipResult.Status, shipResult.Errors)
	}

	// Flush every trace feed, then read the fleet back from the collector: all three services
	// registered, healthy, with traced invocations and no missing feeds.
	for _, svc := range []*service{shipping, payments, orders} {
		svc.exporter.Close()
	}
	collectorClient := httpclient.NewClient(collectorURL)
	fleetResult := collectorClient.Send(ctx, benzene.NewTopic(mesh.TopicQueryFleet), nil, []byte(`{}`))
	fleet, err := httpclient.Unmarshal[meshd.FleetView](fleetResult)
	if err != nil || fleet.Payload == nil {
		t.Fatalf("fleet query: %v %v", fleetResult.Status, err)
	}

	services := map[string]meshd.ServiceSummary{}
	for _, svc := range fleet.Payload.Services {
		services[svc.Service] = svc
	}
	for _, name := range []string{"orders", "payments", "shipping"} {
		svc, ok := services[name]
		if !ok {
			t.Fatalf("fleet is missing %q: %+v", name, fleet.Payload.Services)
		}
		if svc.Health != "healthy" {
			t.Errorf("%s health = %q, want healthy", name, svc.Health)
		}
		if len(svc.MissingFeeds) != 0 {
			t.Errorf("%s missingFeeds = %v, want none", name, svc.MissingFeeds)
		}
	}
	if services["orders"].Invocations == 0 || services["payments"].Invocations == 0 || services["shipping"].Invocations == 0 {
		t.Errorf("expected traced invocations on every hop, got %+v", services)
	}

	// Consumer edges were derived from trace parentage - orders' outbound call is what puts it
	// on payment:take's consumers, not any declaration.
	topicResult := collectorClient.Send(ctx, benzene.NewTopic(mesh.TopicQueryTopic), nil, []byte(`{"topic":"payment:take"}`))
	paymentTopic, err := httpclient.Unmarshal[meshd.TopicSummary](topicResult)
	if err != nil || paymentTopic.Payload == nil {
		t.Fatalf("topic query: %v %v", topicResult.Status, err)
	}
	if len(paymentTopic.Payload.Consumers) != 1 || paymentTopic.Payload.Consumers[0] != "orders" {
		t.Errorf("payment:take consumers = %v, want [orders]", paymentTopic.Payload.Consumers)
	}
}

// TestNewService_Standalone proves a service with no downstream and no collector still serves
// traffic identically — the "unwired" posture every service starts from before its env vars are
// set (e.g. examples/k8s-mesh-helloworld/README.md's standalone run), matching
// examples/mesh-helloworld's degradation-rule test.
func TestNewService_Standalone(t *testing.T) {
	svc := newService("shipping", "", "")
	if svc.collector != nil || svc.exporter != nil || svc.issueExporter != nil {
		t.Fatalf("standalone service should have no collector/exporter wiring, got collector=%v exporter=%v issueExporter=%v", svc.collector, svc.exporter, svc.issueExporter)
	}
	server := httptest.NewServer(svc.handler)
	defer server.Close()
	defer svc.exporter.Close() // nil-safe
	defer svc.issueExporter.Close()

	resp, err := http.Post(server.URL+"/shipments", "application/json",
		strings.NewReader(`{"orderId":"o-1","address":"x","carrier":"y"}`))
	if err != nil {
		t.Fatalf("POST /shipments: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /shipments = %d %s", resp.StatusCode, body)
	}

	// announceAndHeartbeat must be a safe no-op with no collector configured.
	svc.announceAndHeartbeat(context.Background())
}

// TestNewService_DefaultsToOrders proves an unrecognised/empty MESH_SERVICE value falls back to
// orders, matching benzene-dotnet's Domain.HandlersFor switch default.
func TestNewService_DefaultsToOrders(t *testing.T) {
	for _, in := range []string{"", "bogus", "orders"} {
		svc := newService(in, "", "")
		if svc.name != "orders" {
			t.Errorf("newService(%q, ...).name = %q, want orders", in, svc.name)
		}
	}
}
