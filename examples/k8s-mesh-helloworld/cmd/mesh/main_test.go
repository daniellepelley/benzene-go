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
)

// TestDiscoveredHandler_EmptyFleet proves /mesh/discovered reads the collector's own fleet view
// (zero services registered yet -> {"discovered":0}), independent of the service binary.
func TestDiscoveredHandler_EmptyFleet(t *testing.T) {
	server := httptest.NewServer(newMeshApp())
	defer server.Close()

	resp, err := http.Get(server.URL + "/mesh/discovered")
	if err != nil {
		t.Fatalf("GET /mesh/discovered: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got discoveredResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if got.Discovered != 0 {
		t.Errorf("Discovered = %d, want 0 (nothing has registered yet)", got.Discovered)
	}
}

// TestDiscoveredHandler_CountsRegisteredServices proves /mesh/discovered counts what has
// actually registered over the wire envelope — a real POST to benzene:mesh:register, not a
// hand-inspected store.
func TestDiscoveredHandler_CountsRegisteredServices(t *testing.T) {
	server := httptest.NewServer(newMeshApp())
	defer server.Close()
	envelopeClient := httpclient.NewClient(server.URL + httpbinding.EnvelopePath)

	for _, name := range []string{"orders", "payments", "shipping"} {
		desc := mesh.Descriptor{Service: name, Runtime: "go"}
		body, _ := json.Marshal(desc)
		if result := envelopeClient.Send(context.Background(), benzene.NewTopic(mesh.TopicRegister), nil, body); !result.IsSuccessful() {
			t.Fatalf("register %s: %v %v", name, result.Status, result.Errors)
		}
	}

	resp, err := http.Get(server.URL + "/mesh/discovered")
	if err != nil {
		t.Fatalf("GET /mesh/discovered: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got discoveredResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if got.Discovered != 3 {
		t.Errorf("Discovered = %d, want 3", got.Discovered)
	}
}

// TestMeshView_Redirects proves GET / redirects to the Mesh View, and the view itself answers.
func TestMeshView_Redirects(t *testing.T) {
	server := httptest.NewServer(newMeshApp())
	defer server.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("location") != "/benzene/fleet-ui" {
		t.Errorf("GET / = %d location=%q, want 302 to /benzene/fleet-ui", resp.StatusCode, resp.Header.Get("location"))
	}

	view, err := http.Get(server.URL + "/benzene/fleet-ui")
	if err != nil {
		t.Fatalf("GET /benzene/fleet-ui: %v", err)
	}
	defer view.Body.Close()
	viewBody, _ := io.ReadAll(view.Body)
	if view.StatusCode != http.StatusOK || !strings.Contains(string(viewBody), "Benzene Mesh") {
		t.Errorf("GET /benzene/fleet-ui = %d, want 200 with the Mesh View page", view.StatusCode)
	}
}
