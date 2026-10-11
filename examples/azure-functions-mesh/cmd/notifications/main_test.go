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

func TestNotifications_OrderPlaced_Acknowledges(t *testing.T) {
	app := newApp(nil)
	body, err := json.Marshal(map[string]any{"orderId": "order-1"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	rec := invokeEventHubBatch(t, mux(app), "/OrderPlaced", "order:placed", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

func TestNotifications_IntegrationEvents_AcknowledgesBothEventTypes(t *testing.T) {
	app := newApp(nil)
	handler := mux(app)

	rec := invokeEventGrid(t, handler, "/IntegrationEvents", "payment:captured", map[string]any{"paymentId": "pay-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("payment:captured outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	rec = invokeEventGrid(t, handler, "/IntegrationEvents", "shipment:dispatched", map[string]any{"shipmentId": "ship-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("shipment:dispatched outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

func TestNotifications_HealthCheck(t *testing.T) {
	app := newApp(nil)
	res := invokeHTTP(t, mux(app), "/Health", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Errorf("GET /Health statusCode = %s, want 200", res.StatusCode)
	}
}

func TestNotifications_Spec(t *testing.T) {
	app := newApp(nil)
	res := invokeHTTP(t, mux(app), "/Spec", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Fatalf("GET /Spec statusCode = %s, want 200", res.StatusCode)
	}
	if !strings.Contains(res.Body, `"service":"notifications"`) {
		t.Errorf("spec body = %s, want it to describe the notifications service", res.Body)
	}
}

// TestNotifications_ContractDeclaresWhatItSends pins notifications as a pure event consumer: it sends
// nothing, so its document must carry no events or sends - and, just as importantly, must not
// name them in degraded. "Sends nothing" and "send side wasn't wired up" are
// different claims about this service, and only the first one is true here.
func TestNotifications_ContractDeclaresWhatItSends(t *testing.T) {
	doc := newApp(nil).Contract()

	// What it provides: its events and its sends, sorted by topic.
	got := []string{}
	for _, event := range doc.Events {
		got = append(got, event.Topic)
	}
	for _, send := range doc.Sends {
		got = append(got, send.Topic)
	}
	slices.Sort(got)
	if want := []string{}; !slices.Equal(got, want) {
		t.Errorf("events and sends = %v, want %v", got, want)
	}
	if len(doc.Degraded) != 0 {
		t.Errorf("Degraded = %v, want none (events and sends are declared)", doc.Degraded)
	}
}
