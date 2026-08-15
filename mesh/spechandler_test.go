package mesh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestSpecHandler_GetServesTheDerivedDescriptor(t *testing.T) {
	descriptor := Describe(newTestRegistry(t, benzene.NewTopic("order:create")), nil, ServiceInfo{Service: "orders"})
	handler := SpecHandler(descriptor)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SpecPathForTest, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// The spec must parse as a non-empty JSON object with the derived catalog - exactly what an
	// external probe checks for R5, and enough to reconstruct the service identity + topics.
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("spec body is not a JSON object: %v; body = %s", err, rec.Body)
	}
	if doc["service"] != "orders" {
		t.Errorf("spec service = %v, want orders", doc["service"])
	}
	topics, ok := doc["topics"].([]any)
	if !ok || len(topics) != 1 {
		t.Errorf("spec topics = %v, want one derived topic", doc["topics"])
	}
}

func TestSpecHandler_NonGetIsMethodNotAllowed(t *testing.T) {
	handler := SpecHandler(Describe(nil, nil, ServiceInfo{Service: "orders"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, SpecPathForTest, nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for a non-GET request", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow = %q, want GET", allow)
	}
}

// SpecPathForTest is a local copy of the standard spec mount path; the mesh package does not depend
// on httpbinding (that would be a cycle), so the handler is path-agnostic and the test supplies one.
const SpecPathForTest = "/benzene/spec"
