package main

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

func TestNewHandler_FunctionURLEventReturnsGreeting(t *testing.T) {
	handler := newHandler(newApp())

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"{\"name\":\"World\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp struct {
		StatusCode int    `json:"statusCode"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v; result = %s", err, result)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("statusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(resp.Body) error = %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestNewHandler_FunctionURLMissingNameIsBadRequest(t *testing.T) {
	handler := newHandler(newApp())

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"{\"name\":\"\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		StatusCode int `json:"statusCode"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("statusCode = %d, want 400", resp.StatusCode)
	}
}

func TestNewHandler_EnvelopeEventRoundTrip(t *testing.T) {
	handler := newHandler(newApp())

	envReq, err := wire.MarshalRequest(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"Envelope"}`})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}

	result, err := handler(context.Background(), envReq)
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	resp, err := wire.UnmarshalResponse(result)
	if err != nil {
		t.Fatalf("UnmarshalResponse() error = %v; result = %s", err, result)
	}
	if resp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusOk)
	}
}

func TestNewHandler_MalformedEventIsError(t *testing.T) {
	handler := newHandler(newApp())

	// Neither a valid HTTP-v2 event (no requestContext) nor a valid envelope - falls through
	// to EnvelopeHandler, which reports it as an error.
	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}
