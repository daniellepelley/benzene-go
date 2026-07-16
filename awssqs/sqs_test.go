package awssqs

import (
	"context"
	"encoding/json"
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

func invoke(t *testing.T, builder *benzene.ApplicationBuilder, event string) batchResponse {
	t.Helper()
	handler := Handler(builder)
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp batchResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v; result = %s", err, result)
	}
	return resp
}

func TestHandler_TopicFromMessageAttributeSucceeds(t *testing.T) {
	builder := newTestBuilder(t)
	event := `{"Records":[{"messageId":"msg-1","body":"{\"name\":\"World\"}","messageAttributes":{"topic":{"stringValue":"greet","dataType":"String"}}}]}`

	resp := invoke(t, builder, event)

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}
}

func TestHandler_FailedRecordIsReportedAsBatchItemFailure(t *testing.T) {
	builder := newTestBuilder(t)
	event := `{"Records":[{"messageId":"msg-1","body":"{\"name\":\"\"}","messageAttributes":{"topic":{"stringValue":"greet"}}}]}`

	resp := invoke(t, builder, event)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
		t.Errorf("BatchItemFailures = %v, want [{msg-1}]", resp.BatchItemFailures)
	}
}

func TestHandler_OnlyFailedRecordInMixedBatchIsReported(t *testing.T) {
	builder := newTestBuilder(t)
	event := `{"Records":[
		{"messageId":"msg-ok","body":"{\"name\":\"World\"}","messageAttributes":{"topic":{"stringValue":"greet"}}},
		{"messageId":"msg-bad","body":"{\"name\":\"\"}","messageAttributes":{"topic":{"stringValue":"greet"}}}
	]}`

	resp := invoke(t, builder, event)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-bad" {
		t.Errorf("BatchItemFailures = %v, want [{msg-bad}]", resp.BatchItemFailures)
	}
}

func TestHandler_MissingHandlerIsReportedAsFailure(t *testing.T) {
	builder := newTestBuilder(t)
	event := `{"Records":[{"messageId":"msg-1","body":"{}","messageAttributes":{"topic":{"stringValue":"no:such:topic"}}}]}`

	resp := invoke(t, builder, event)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
		t.Errorf("BatchItemFailures = %v, want [{msg-1}]", resp.BatchItemFailures)
	}
}

func TestHandler_EnvelopeFallbackWhenNoTopicAttribute(t *testing.T) {
	builder := newTestBuilder(t)
	envReq, err := wire.MarshalRequest(wire.Request{Topic: "greet", Headers: map[string]string{"x-envelope": "1"}, Body: `{"name":"Envelope"}`})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}
	event := `{"Records":[{"messageId":"msg-1","body":` + mustQuote(t, string(envReq)) + `,"messageAttributes":{}}]}`

	resp := invoke(t, builder, event)

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}
}

func TestHandler_NeitherAttributeNorEnvelopeIsValidationFailure(t *testing.T) {
	builder := newTestBuilder(t)
	event := `{"Records":[{"messageId":"msg-1","body":"not json and no topic attribute","messageAttributes":{}}]}`

	resp := invoke(t, builder, event)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
		t.Errorf("BatchItemFailures = %v, want [{msg-1}] (empty topic -> ValidationError)", resp.BatchItemFailures)
	}
}

func TestHandler_MalformedEventIsError(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}

func TestHandler_EmptyBatchProducesNoFailures(t *testing.T) {
	builder := newTestBuilder(t)

	resp := invoke(t, builder, `{"Records":[]}`)

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none for an empty batch", resp.BatchItemFailures)
	}
}

func TestResolveRequest_NonTopicAttributesBecomeHeaders(t *testing.T) {
	record := sqsRecord{
		MessageID: "msg-1",
		Body:      "body",
		MessageAttributes: map[string]sqsMessageAttribute{
			"topic":         {StringValue: "greet"},
			"x-correlation": {StringValue: "abc"},
		},
	}

	req := resolveRequest(record)

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

func mustQuote(t *testing.T, s string) string {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(data)
}
