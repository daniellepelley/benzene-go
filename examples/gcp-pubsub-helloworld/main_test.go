package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pushBody(t *testing.T, attributes map[string]string, data string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"data":       base64.StdEncoding.EncodeToString([]byte(data)),
			"attributes": attributes,
		},
		"subscription": "projects/p/subscriptions/greet-helloworld-push",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(body)
}

func TestPushEndpoint_GreetMessageIsAcked(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Post(server.URL+"/pubsub", "application/json",
		strings.NewReader(pushBody(t, map[string]string{"topic": "greet"}, `{"name":"World"}`)))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d (ack)", resp.StatusCode, http.StatusNoContent)
	}
}

func TestPushEndpoint_FailedMessageIsNacked(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Post(server.URL+"/pubsub", "application/json",
		strings.NewReader(pushBody(t, map[string]string{"topic": "greet"}, `{"name":""}`)))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (nack)", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestOtherPathsAreNotFound(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
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
