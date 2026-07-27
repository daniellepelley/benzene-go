package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// The ack/nack tests boot the real app from its composition root and push a native Pub/Sub push
// delivery in the front door with benzenetest.SendPubSub - the same harness shape as the AWS
// consumers, only the Send* call differs. A success status acks (204); a failure nacks (500),
// handing the message back for redelivery.

func TestPushEndpoint_GreetMessageIsAcked(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := benzenetest.SendPubSub(t, host, benzene.NewTopic("greet"), greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d (ack)", resp.StatusCode, http.StatusNoContent)
	}
}

func TestPushEndpoint_FailedMessageIsNacked(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := benzenetest.SendPubSub(t, host, benzene.NewTopic("greet"), greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (nack)", resp.StatusCode, http.StatusInternalServerError)
	}
}

// This tests the mux routing in newHandler (app-specific glue), so it stays an httptest against
// the assembled handler rather than a single-transport Send.
func TestOtherPathsAreNotFound(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp().Run()))
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
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
