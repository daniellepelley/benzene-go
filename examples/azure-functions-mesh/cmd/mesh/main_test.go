package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daniellepelley/benzene-go/meshd"
)

type azureHTTPOutput struct {
	StatusCode string            `json:"statusCode"`
	Body       string            `json:"body"`
	Headers    map[string]string `json:"headers"`
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

func TestMesh_FleetUi_ServesTheView(t *testing.T) {
	handler := mux(meshd.New(meshd.Options{}))
	res := invokeHTTP(t, handler, "/FleetUi", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Fatalf("GET /FleetUi statusCode = %s, want 200", res.StatusCode)
	}
	if !strings.Contains(res.Body, "<html") {
		t.Errorf("body = %q, want the Mesh View HTML", res.Body)
	}
	if !strings.Contains(res.Headers["content-type"], "text/html") {
		t.Errorf("content-type = %q, want text/html", res.Headers["content-type"])
	}
}

func TestMesh_Invoke_RegisterThenDiscoveredReportsIt(t *testing.T) {
	collector := meshd.New(meshd.Options{})
	handler := mux(collector)

	descriptor, err := json.Marshal(map[string]any{"service": "orders", "instanceId": "orders", "runtime": "go", "topics": []any{}})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	envelope, err := json.Marshal(map[string]any{"topic": "benzene:mesh:register", "headers": map[string]string{}, "body": string(descriptor)})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	res := invokeHTTP(t, handler, "/Invoke", http.MethodPost, string(envelope))
	if res.StatusCode != "200" {
		t.Fatalf("register outer envelope statusCode = %s, want 200; body = %s", res.StatusCode, res.Body)
	}

	discovered := invokeHTTP(t, handler, "/Discovered", http.MethodGet, "")
	if discovered.StatusCode != "200" {
		t.Fatalf("GET /Discovered statusCode = %s, want 200; body = %s", discovered.StatusCode, discovered.Body)
	}
	var count struct {
		Discovered int `json:"discovered"`
	}
	if err := json.Unmarshal([]byte(discovered.Body), &count); err != nil {
		t.Fatalf("unmarshal discovered response: %v; body = %s", err, discovered.Body)
	}
	if count.Discovered != 1 {
		t.Errorf("Discovered = %d, want 1 after one service registered", count.Discovered)
	}
}

func TestMesh_Discovered_ZeroBeforeAnyoneRegisters(t *testing.T) {
	handler := mux(meshd.New(meshd.Options{}))
	res := invokeHTTP(t, handler, "/Discovered", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Fatalf("statusCode = %s, want 200", res.StatusCode)
	}
	var count struct {
		Discovered int `json:"discovered"`
	}
	if err := json.Unmarshal([]byte(res.Body), &count); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if count.Discovered != 0 {
		t.Errorf("Discovered = %d, want 0", count.Discovered)
	}
}

func TestPortFromEnv_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("FUNCTIONS_CUSTOMHANDLER_PORT", "")
	if got := portFromEnv(); got != "8080" {
		t.Errorf("portFromEnv() = %q, want %q", got, "8080")
	}
}

func TestPortFromEnv_UsesEnvWhenSet(t *testing.T) {
	t.Setenv("FUNCTIONS_CUSTOMHANDLER_PORT", "9090")
	if got := portFromEnv(); got != "9090" {
		t.Errorf("portFromEnv() = %q, want %q", got, "9090")
	}
}
