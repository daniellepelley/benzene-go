package awslambda

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
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

func asyncEnvelope(t *testing.T, topic, body string) json.RawMessage {
	t.Helper()
	data, err := wire.MarshalRequest(wire.Request{Topic: topic, Headers: map[string]string{}, Body: body})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}
	return data
}

func TestAsyncEnvelopeHandler_SuccessCompletesTheInvocation(t *testing.T) {
	result, err := AsyncEnvelopeHandler(newTestBuilder(t))(context.Background(), asyncEnvelope(t, "greet", `{"name":"World"}`))
	if err != nil {
		t.Fatalf("handler() error = %v, want nil for a successful envelope", err)
	}
	if resp, err := wire.UnmarshalResponse(result); err != nil || resp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("response = %s (%v), want the ok envelope", result, err)
	}
}

// transport-bindings.md §1: an asynchronous invocation has no caller to read a failure envelope, so
// a failure must fail the invocation for Lambda to retry it - the bug EnvelopeHandler had there.
func TestAsyncEnvelopeHandler_FailureFailsTheInvocation(t *testing.T) {
	handler := AsyncEnvelopeHandler(newTestBuilder(t))
	for _, topic := range []string{"no:such:topic", ""} {
		if _, err := handler(context.Background(), asyncEnvelope(t, topic, "")); err == nil {
			t.Errorf("handler(%q) error = nil, want a Go error so Lambda's async retry applies", topic)
		}
	}
	if _, err := handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("handler(malformed) error = nil, want a Go error")
	}
}

func TestAsyncEnvelopeHandler_Settlement(t *testing.T) {
	atMostOnce := AsyncEnvelopeHandler(newTestBuilder(t), WithSettlement(envelope.Settlement{Delivery: envelope.AtMostOnce}))
	if _, err := atMostOnce(context.Background(), asyncEnvelope(t, "", "")); err != nil {
		t.Errorf("at-most-once handler error = %v, want nil whatever the outcome", err)
	}

	ignoring := AsyncEnvelopeHandler(newTestBuilder(t), WithSettlement(envelope.Settlement{IgnoreUnmatchedTopics: true}))
	if _, err := ignoring(context.Background(), asyncEnvelope(t, "no:such:topic", "")); err != nil {
		t.Errorf("unmatched topic with IgnoreUnmatchedTopics error = %v, want nil", err)
	}
	if _, err := ignoring(context.Background(), asyncEnvelope(t, "", "")); err == nil {
		t.Error("an empty topic is a validation-error, not an unmatched topic: want a Go error")
	}
}
