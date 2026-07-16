package awssns

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func TestHandler_TopicFromMessageAttributeSucceeds(t *testing.T) {
	handler := Handler(newTestBuilder(t))
	event := `{"Records":[{"Sns":{"MessageId":"msg-1","Message":"{\"name\":\"World\"}","MessageAttributes":{"topic":{"Value":"greet"}}}}]}`

	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Errorf("handler() error = %v, want nil", err)
	}
}

func TestHandler_FailedNotificationReturnsError(t *testing.T) {
	handler := Handler(newTestBuilder(t))
	event := `{"Records":[{"Sns":{"MessageId":"msg-1","Message":"{\"name\":\"\"}","MessageAttributes":{"topic":{"Value":"greet"}}}}]}`

	_, err := handler(context.Background(), json.RawMessage(event))
	if err == nil {
		t.Fatal("handler() error = nil, want an error for a failed notification")
	}
	if !strings.Contains(err.Error(), "msg-1") {
		t.Errorf("error = %v, want it to mention the failed message ID", err)
	}
}

func TestHandler_OnlyFailedNotificationIsNamedInMultiRecordError(t *testing.T) {
	handler := Handler(newTestBuilder(t))
	event := `{"Records":[
		{"Sns":{"MessageId":"msg-ok","Message":"{\"name\":\"World\"}","MessageAttributes":{"topic":{"Value":"greet"}}}},
		{"Sns":{"MessageId":"msg-bad","Message":"{\"name\":\"\"}","MessageAttributes":{"topic":{"Value":"greet"}}}}
	]}`

	_, err := handler(context.Background(), json.RawMessage(event))
	if err == nil {
		t.Fatal("handler() error = nil, want an error")
	}
	if strings.Contains(err.Error(), "msg-ok") {
		t.Errorf("error = %v, should not mention the successful message", err)
	}
	if !strings.Contains(err.Error(), "msg-bad") {
		t.Errorf("error = %v, want it to mention msg-bad", err)
	}
}

func TestHandler_MissingHandlerReturnsError(t *testing.T) {
	handler := Handler(newTestBuilder(t))
	event := `{"Records":[{"Sns":{"MessageId":"msg-1","Message":"{}","MessageAttributes":{"topic":{"Value":"no:such:topic"}}}}]}`

	if _, err := handler(context.Background(), json.RawMessage(event)); err == nil {
		t.Error("handler() error = nil, want an error for a missing handler")
	}
}

func TestHandler_EnvelopeFallbackWhenNoTopicAttribute(t *testing.T) {
	envReq, err := wire.MarshalRequest(wire.Request{Topic: "greet", Headers: map[string]string{"x-envelope": "1"}, Body: `{"name":"Envelope"}`})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}
	messageJSON, err := json.Marshal(string(envReq))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	handler := Handler(newTestBuilder(t))
	event := `{"Records":[{"Sns":{"MessageId":"msg-1","Message":` + string(messageJSON) + `,"MessageAttributes":{}}}]}`

	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Errorf("handler() error = %v, want nil", err)
	}
}

func TestHandler_NeitherAttributeNorEnvelopeReturnsError(t *testing.T) {
	handler := Handler(newTestBuilder(t))
	event := `{"Records":[{"Sns":{"MessageId":"msg-1","Message":"not json and no topic attribute","MessageAttributes":{}}}]}`

	if _, err := handler(context.Background(), json.RawMessage(event)); err == nil {
		t.Error("handler() error = nil, want an error (empty topic -> ValidationError)")
	}
}

func TestHandler_MalformedEventIsError(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}

func TestHandler_EmptyBatchProducesNoError(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	if _, err := handler(context.Background(), json.RawMessage(`{"Records":[]}`)); err != nil {
		t.Errorf("handler() error = %v, want nil for an empty batch", err)
	}
}

func TestResolveRequest_NonTopicAttributesBecomeHeaders(t *testing.T) {
	sns := snsMessage{
		MessageID: "msg-1",
		Message:   "body",
		MessageAttributes: map[string]snsMessageAttribute{
			"topic":         {Value: "greet"},
			"x-correlation": {Value: "abc"},
		},
	}

	req := resolveRequest(sns)

	if req.Topic != "greet" {
		t.Errorf("Topic = %q, want %q", req.Topic, "greet")
	}
	if req.Headers["x-correlation"] != "abc" {
		t.Errorf(`Headers["x-correlation"] = %q, want "abc"`, req.Headers["x-correlation"])
	}
	if _, ok := req.Headers["topic"]; ok {
		t.Error(`Headers should not contain "topic" - it's consumed as routing info, not a header`)
	}
}
