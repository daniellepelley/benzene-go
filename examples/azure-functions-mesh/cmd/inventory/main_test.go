package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type azureHTTPOutput struct {
	StatusCode string `json:"statusCode"`
	Body       string `json:"body"`
}

func invokeHTTP(t *testing.T, handler http.Handler, localPath, method, body string) azureHTTPOutput {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"Data":     map[string]any{"req": map[string]any{"Method": method, "Headers": map[string]string{}, "Body": body}},
		"Metadata": map[string]any{},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, localPath, strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var inv struct {
		Outputs map[string]azureHTTPOutput `json:"Outputs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rec.Body.String())
	}
	res, ok := inv.Outputs["res"]
	if !ok {
		t.Fatalf("Outputs missing \"res\"; body = %s", rec.Body.String())
	}
	return res
}

// invokeEventHubBatch drives the Event Hub trigger local path with one event carrying topic in
// its application properties, matching azureeventhub.Client's own publish shape.
func invokeEventHubBatch(t *testing.T, handler http.Handler, localPath, topic, body string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"Data":     map[string]any{"eventHubMessages": []string{body}},
		"Metadata": map[string]any{"PropertiesArray": []map[string]string{{"topic": topic}}},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, localPath, strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// invokeEventGrid drives the Event Grid trigger local path with one Event Grid-schema event.
func invokeEventGrid(t *testing.T, handler http.Handler, localPath, eventType string, data map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"Data": map[string]any{"eventGridEvent": map[string]any{"eventType": eventType, "data": data}},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, localPath, strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestInventory_OrderPlaced_Acknowledges(t *testing.T) {
	app := newApp(nil)
	body, err := json.Marshal(map[string]any{"orderId": "order-1", "customerId": "cust-1", "sku": "espresso", "quantity": 2})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	rec := invokeEventHubBatch(t, mux(app), "/OrderPlaced", "order:placed", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

func TestInventory_ShipmentDispatched_Acknowledges(t *testing.T) {
	app := newApp(nil)
	rec := invokeEventGrid(t, mux(app), "/ShipmentDispatched", "shipment:dispatched", map[string]any{"shipmentId": "ship-1", "orderId": "order-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

func TestInventory_HealthCheck(t *testing.T) {
	app := newApp(nil)
	res := invokeHTTP(t, mux(app), "/Health", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Errorf("GET /Health statusCode = %s, want 200", res.StatusCode)
	}
}

func TestInventory_Spec(t *testing.T) {
	app := newApp(nil)
	res := invokeHTTP(t, mux(app), "/Spec", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Fatalf("GET /Spec statusCode = %s, want 200", res.StatusCode)
	}
	if !strings.Contains(res.Body, `"service":"inventory"`) {
		t.Errorf("spec body = %s, want it to describe the inventory service", res.Body)
	}
}

// TestInventory_DescriptorDeclaresWhatItSends pins inventory as a pure event consumer: it sends
// nothing, so its descriptor must carry an EMPTY Consumes - and, just as importantly, must not
// report the outbound feed as degraded. "Sends nothing" and "send side wasn't wired up" are
// different claims about this service, and only the first one is true here.
func TestInventory_DescriptorDeclaresWhatItSends(t *testing.T) {
	desc := newApp(nil).Descriptor()

	got := []string{}
	for _, topic := range desc.Produces {
		got = append(got, topic.ID)
	}
	// Sorted by topic ID, matching mesh.OutboundRegistry.Topics().
	if want := []string{}; !slices.Equal(got, want) {
		t.Errorf("Descriptor.Produces = %v, want %v", got, want)
	}
	if len(desc.Degraded) != 0 {
		t.Errorf("Descriptor.Degraded = %v, want none (both feeds are wired)", desc.Degraded)
	}
}
