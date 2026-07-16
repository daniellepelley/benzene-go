package awslambda

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

func TestEnvelopeHandler_RoundTrip(t *testing.T) {
	handler := EnvelopeHandler(newTestBuilder(t))

	envReq, err := wire.MarshalRequest(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"World"}`})
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

func TestEnvelopeHandler_MissingHandlerIsNotFound(t *testing.T) {
	handler := EnvelopeHandler(newTestBuilder(t))

	envReq, err := wire.MarshalRequest(wire.Request{Topic: "no:such:topic", Headers: map[string]string{}, Body: ""})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}

	result, err := handler(context.Background(), envReq)
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	resp, err := wire.UnmarshalResponse(result)
	if err != nil {
		t.Fatalf("UnmarshalResponse() error = %v", err)
	}
	if resp.StatusCode != string(benzene.StatusNotFound) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusNotFound)
	}
}

func TestEnvelopeHandler_MalformedEventIsError(t *testing.T) {
	handler := EnvelopeHandler(newTestBuilder(t))

	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}
