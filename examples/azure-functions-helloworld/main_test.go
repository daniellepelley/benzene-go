package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type invocationResponse struct {
	Outputs map[string]struct {
		StatusCode string `json:"statusCode"`
		Body       string `json:"body"`
	} `json:"Outputs"`
}

func invoke(t *testing.T, handler http.Handler, name string) invocationResponse {
	t.Helper()
	body := `{"Data":{"req":{"Method":"POST","Headers":{},"Body":"{\"name\":\"` + name + `\"}"}},"Metadata":{}}`
	req := httptest.NewRequest(http.MethodPost, "/Greet", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var inv invocationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, rec.Body.String())
	}
	return inv
}

func TestNewHandler_ReturnsGreeting(t *testing.T) {
	handler := newHandler(newApp())

	inv := invoke(t, handler, "World")

	res := inv.Outputs["res"]
	if res.StatusCode != "200" {
		t.Fatalf("res.StatusCode = %q, want %q; body = %s", res.StatusCode, "200", res.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(res.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(res.Body) error = %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestNewHandler_MissingNameIsBadRequest(t *testing.T) {
	handler := newHandler(newApp())

	inv := invoke(t, handler, "")

	if inv.Outputs["res"].StatusCode != "400" {
		t.Errorf("res.StatusCode = %q, want %q", inv.Outputs["res"].StatusCode, "400")
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
