package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests deliberately drive the deployed handler through raw net/http + httptest rather than
// the benzenetest harness, because that IS the point of the Cloud Run example: a plain HTTP server
// with no benzene-specific binding. examples/helloworld shows the same greet handler tested through
// the benzenetest harness (SendHTTP), so the two examples together show both paths on purpose - the
// harness for the transport-parallel story, raw net/http for "Cloud Run is just an http.Handler".

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
