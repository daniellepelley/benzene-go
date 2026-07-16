package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

type fakeSendMessageAPI struct {
	err error
}

func (f *fakeSendMessageAPI) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sqs.SendMessageOutput{}, nil
}

func newTestHandler(api *fakeSendMessageAPI) awslambda.HandlerFunc {
	sqsClient := awssqs.NewClient(api, "https://sqs.example/queue")
	builder := newApp(sqsClient)
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	return awslambda.HTTPHandler(builder, routes)
}

func TestPublisher_ForwardsToQueueAndReturnsAccepted(t *testing.T) {
	handler := newTestHandler(&fakeSendMessageAPI{})

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"{\"name\":\"World\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp struct {
		StatusCode int `json:"statusCode"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("statusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
}

func TestPublisher_DoesNotValidateContentBeforeForwarding(t *testing.T) {
	// The publisher's job is only to forward to the queue - it deliberately does not
	// duplicate the consumer's own validation (greeting.Handler's "name is required" check).
	// An empty name is still accepted here; it's the consumer, processing the message later
	// and asynchronously, that reports the actual validation failure - invisible to this
	// original HTTP caller, an inherent tradeoff of fire-and-forget messaging.
	handler := newTestHandler(&fakeSendMessageAPI{})

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"{\"name\":\"\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp struct {
		StatusCode int `json:"statusCode"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("statusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
}

func TestPublisher_QueueFailureIsServiceUnavailable(t *testing.T) {
	handler := newTestHandler(&fakeSendMessageAPI{err: errors.New("boom")})

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"{\"name\":\"World\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp struct {
		StatusCode int `json:"statusCode"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("statusCode = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}
