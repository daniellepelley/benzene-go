package awseventbridge

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

func TestHandler_EnvelopeDetailDispatches(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	detail, err := json.Marshal(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"Bus"}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	event := `{"id":"evt-1","detail-type":"greet","source":"com.example.orders","detail":` + string(detail) + `}`

	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Errorf("handler() error = %v, want nil for a successful dispatch", err)
	}
}

func TestHandler_DetailTypeTopicDispatchesRawDetail(t *testing.T) {
	// A non-Benzene producer: detail is a plain domain object, topic from detail-type.
	handler := Handler(newTestBuilder(t))

	event := `{"id":"evt-2","detail-type":"greet","source":"aws.partner","detail":{"name":"Partner"}}`
	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Errorf("handler() error = %v, want nil", err)
	}
}

func TestHandler_FailuresReturnGoError(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "handler failure status", event: `{"id":"evt-3","detail-type":"greet","source":"s","detail":{"name":""}}`},
		{name: "no topic resolvable", event: `{"id":"evt-4","source":"s","detail":{"name":"x"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := Handler(newTestBuilder(t))
			if _, err := handler(context.Background(), json.RawMessage(tt.event)); err == nil {
				t.Error("handler() error = nil, want an error to trigger the async-invoke retry")
			}
		})
	}
}

func TestHandler_MalformedEventIsError(t *testing.T) {
	handler := Handler(newTestBuilder(t))
	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}

func TestResolveRequest(t *testing.T) {
	envelopeDetail, err := json.Marshal(wire.Request{
		Topic:   "greet",
		Headers: map[string]string{"x-correlation-id": "abc", "source": "from-envelope"},
		Body:    `{}`,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	tests := []struct {
		name        string
		rule        ruleEvent
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name: "envelope detail wins - headers merge, envelope's same-name header beats the event field",
			rule: ruleEvent{ID: "evt-1", DetailType: "other", Source: "com.example", Detail: envelopeDetail},
			// The envelope's topic wins over detail-type: the envelope is the Benzene-native
			// channel and carries strictly more information.
			wantTopic:   "greet",
			wantBody:    `{}`,
			wantHeaders: map[string]string{"id": "evt-1", "source": "from-envelope", "x-correlation-id": "abc"},
		},
		{
			name:        "plain detail uses detail-type and verbatim body",
			rule:        ruleEvent{ID: "evt-2", DetailType: "greet", Source: "aws.partner", Detail: json.RawMessage(`{"name":"x"}`)},
			wantTopic:   "greet",
			wantBody:    `{"name":"x"}`,
			wantHeaders: map[string]string{"id": "evt-2", "source": "aws.partner"},
		},
		{
			name:        "nothing resolvable yields empty topic",
			rule:        ruleEvent{Detail: json.RawMessage(`"plain"`)},
			wantTopic:   "",
			wantBody:    `"plain"`,
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := resolveRequest(tt.rule)
			if req.Topic != tt.wantTopic {
				t.Errorf("Topic = %q, want %q", req.Topic, tt.wantTopic)
			}
			if req.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q", req.Body, tt.wantBody)
			}
			if len(req.Headers) != len(tt.wantHeaders) {
				t.Fatalf("Headers = %v, want %v", req.Headers, tt.wantHeaders)
			}
			for k, v := range tt.wantHeaders {
				if req.Headers[k] != v {
					t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
				}
			}
		})
	}
}

func TestHandler_FailureErrorNamesTheEvent(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	event := `{"id":"evt-9","detail-type":"greet","source":"s","detail":{"name":""}}`
	_, err := handler(context.Background(), json.RawMessage(event))
	if err == nil || !strings.Contains(err.Error(), "evt-9") {
		t.Errorf("handler() error = %v, want it to name event evt-9", err)
	}
}
