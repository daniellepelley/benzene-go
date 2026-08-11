package asyncapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/daniellepelley/benzene-go/asyncapi"
)

func TestHandler_ServesDocumentOnGET(t *testing.T) {
	h := asyncapi.Handler(newDescriptor(t))

	req := httptest.NewRequest(http.MethodGet, "/asyncapi.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if doc["asyncapi"] != "3.0.0" {
		t.Errorf("served asyncapi = %v", doc["asyncapi"])
	}
}

func TestHandler_HEADHasNoBody(t *testing.T) {
	h := asyncapi.Handler(newDescriptor(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/asyncapi.json", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned a body of %d bytes, want 0", rec.Body.Len())
	}
}

func TestHandler_RejectsNonGET(t *testing.T) {
	h := asyncapi.Handler(newDescriptor(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/asyncapi.json", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want 'GET, HEAD'", allow)
	}
}
