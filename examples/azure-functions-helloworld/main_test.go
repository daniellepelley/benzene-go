package main

import (
	"encoding/json"
	"net/http"
	"testing"

	benzenetest "github.com/daniellepelley/benzene-go/benzenetest"
)

// These tests boot the real app from its composition root and push a native Azure Functions
// custom-handler invocation in the front door with benzenetest.SendAzureHTTP - the same harness
// shape as the AWS API Gateway tests, only the Send* call differs (SendAzureHTTP vs
// SendAPIGateway). The framework-mapped status is read out of the Outputs["res"] envelope.

func newTestHost() *benzenetest.Host {
	return benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))
}

func TestNewHandler_ReturnsGreeting(t *testing.T) {
	resp := benzenetest.SendAzureHTTP(t, newTestHost(), http.MethodPost, "/Greet", greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("res.StatusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(res.Body) error = %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestNewHandler_MissingNameIsBadRequest(t *testing.T) {
	resp := benzenetest.SendAzureHTTP(t, newTestHost(), http.MethodPost, "/Greet", greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("res.StatusCode = %d, want 400", resp.StatusCode)
	}
}
