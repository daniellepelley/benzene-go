package openapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/daniellepelley/benzene-go/openapi"
)

func TestHandler_GETServesDocument(t *testing.T) {
	server := httptest.NewServer(openapi.Handler(newDescriptor(t)))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	paths, _ := doc["paths"].(map[string]any)
	if _, ok := paths["/user:create"]; !ok {
		t.Errorf("served document is missing the /user:create path: %v", paths)
	}
}

func TestHandler_HEADHasNoBody(t *testing.T) {
	server := httptest.NewServer(openapi.Handler(newDescriptor(t)))
	defer server.Close()

	req, _ := http.NewRequest(http.MethodHead, server.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD returned a body: %s", body)
	}
}

func TestHandler_RejectsNonGET(t *testing.T) {
	server := httptest.NewServer(openapi.Handler(newDescriptor(t)))
	defer server.Close()

	resp, err := http.Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want \"GET, HEAD\"", allow)
	}
}
