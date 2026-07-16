package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGreetEndpoint_ReturnsGreeting(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Post(server.URL+"/greet", "application/json", strings.NewReader(`{"name":"World"}`))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var greeting greetResponse
	if err := json.NewDecoder(resp.Body).Decode(&greeting); err != nil {
		t.Fatalf("json.Decode() error = %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestGreetEndpoint_MissingNameIsBadRequest(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Post(server.URL+"/greet", "application/json", strings.NewReader(`{"name":""}`))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestPortFromEnv_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("PORT", "")
	if got := portFromEnv(); got != "8080" {
		t.Errorf("portFromEnv() = %q, want %q", got, "8080")
	}
}

func TestPortFromEnv_UsesEnvWhenSet(t *testing.T) {
	t.Setenv("PORT", "9090")
	if got := portFromEnv(); got != "9090" {
		t.Errorf("portFromEnv() = %q, want %q", got, "9090")
	}
}
