package awseventbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
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

func TestHandler_DetailTypeTopicDispatches(t *testing.T) {
	// Topic is detail-type verbatim, per spec - no bolted-on "topic" attribute.
	handler := Handler(newTestBuilder(t))

	event := `{"id":"evt-1","detail-type":"greet","source":"com.example.orders","detail":{"name":"Bus"}}`
	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Errorf("handler() error = %v, want nil for a successful dispatch", err)
	}
}

func TestHandler_EmbeddedHeadersDontBreakRequestMapping(t *testing.T) {
	// The reserved _benzeneHeaders key, when present, is an extra field a request mapper's
	// deserialization simply ignores (per spec) - it must not stop the handler's own
	// request type from binding.
	handler := Handler(newTestBuilder(t))

	event := `{"id":"evt-2","detail-type":"greet","source":"com.example.orders","detail":{"name":"Bus","_benzeneHeaders":{"x-correlation-id":"abc"}}}`
	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Errorf("handler() error = %v, want nil", err)
	}
}

func TestHandler_DetailTypeTopicDispatchesRawDetail(t *testing.T) {
	// A non-Benzene producer: detail is a plain domain object, topic from detail-type.
	handler := Handler(newTestBuilder(t))

	event := `{"id":"evt-3","detail-type":"greet","source":"aws.partner","detail":{"name":"Partner"}}`
	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Errorf("handler() error = %v, want nil", err)
	}
}

func TestHandler_FailuresReturnGoError(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "handler failure status", event: `{"id":"evt-4","detail-type":"greet","source":"s","detail":{"name":""}}`},
		{name: "no topic resolvable", event: `{"id":"evt-5","source":"s","detail":{"name":"x"}}`},
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

func TestHandler_FailureErrorNamesTheEvent(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	event := `{"id":"evt-9","detail-type":"greet","source":"s","detail":{"name":""}}`
	_, err := handler(context.Background(), json.RawMessage(event))
	if err == nil || !strings.Contains(err.Error(), "evt-9") {
		t.Errorf("handler() error = %v, want it to name event evt-9", err)
	}
}

func TestResolveRequest(t *testing.T) {
	tests := []struct {
		name        string
		rule        ruleEvent
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name: "full envelope metadata becomes eventbridge- prefixed headers",
			rule: ruleEvent{
				ID: "evt-1", DetailType: "greet", Source: "com.example",
				Account: "123456789012", Region: "us-east-1", Time: "2026-01-01T00:00:00Z",
				Detail: json.RawMessage(`{"name":"x"}`),
			},
			wantTopic: "greet",
			wantBody:  `{"name":"x"}`,
			wantHeaders: map[string]string{
				"eventbridge-id": "evt-1", "eventbridge-source": "com.example",
				"eventbridge-account": "123456789012", "eventbridge-region": "us-east-1",
				"eventbridge-time": "2026-01-01T00:00:00Z", "eventbridge-detail-type": "greet",
			},
		},
		{
			name:        "embedded _benzeneHeaders lifted out, body stays the raw detail",
			rule:        ruleEvent{ID: "evt-2", DetailType: "greet", Detail: json.RawMessage(`{"name":"x","_benzeneHeaders":{"x-correlation-id":"abc","traceparent":"00-1-2-01"}}`)},
			wantTopic:   "greet",
			wantBody:    `{"name":"x","_benzeneHeaders":{"x-correlation-id":"abc","traceparent":"00-1-2-01"}}`,
			wantHeaders: map[string]string{"eventbridge-id": "evt-2", "eventbridge-detail-type": "greet", "x-correlation-id": "abc", "traceparent": "00-1-2-01"},
		},
		{
			name:        "embedded header wins over eventbridge- prefixed key on collision",
			rule:        ruleEvent{ID: "evt-3", Source: "original-source", DetailType: "greet", Detail: json.RawMessage(`{"eventbridge-source":"ignored","_benzeneHeaders":{"eventbridge-source":"embedded-wins"}}`)},
			wantTopic:   "greet",
			wantBody:    `{"eventbridge-source":"ignored","_benzeneHeaders":{"eventbridge-source":"embedded-wins"}}`,
			wantHeaders: map[string]string{"eventbridge-id": "evt-3", "eventbridge-source": "embedded-wins", "eventbridge-detail-type": "greet"},
		},
		{
			name:        "non-string embedded values are skipped",
			rule:        ruleEvent{ID: "evt-4", DetailType: "greet", Detail: json.RawMessage(`{"_benzeneHeaders":{"good":"kept","bad":3,"worse":{"x":1}}}`)},
			wantTopic:   "greet",
			wantBody:    `{"_benzeneHeaders":{"good":"kept","bad":3,"worse":{"x":1}}}`,
			wantHeaders: map[string]string{"eventbridge-id": "evt-4", "eventbridge-detail-type": "greet", "good": "kept"},
		},
		{
			name:        "non-object detail has no embedded headers to lift",
			rule:        ruleEvent{ID: "evt-5", DetailType: "greet", Detail: json.RawMessage(`"plain"`)},
			wantTopic:   "greet",
			wantBody:    `"plain"`,
			wantHeaders: map[string]string{"eventbridge-id": "evt-5", "eventbridge-detail-type": "greet"},
		},
		{
			name:        "no detail-type yields empty topic",
			rule:        ruleEvent{ID: "evt-6", Detail: json.RawMessage(`{"name":"x"}`)},
			wantTopic:   "",
			wantBody:    `{"name":"x"}`,
			wantHeaders: map[string]string{"eventbridge-id": "evt-6"},
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

func TestEmbeddedHeaders(t *testing.T) {
	tests := []struct {
		name   string
		detail string
		want   map[string]string
	}{
		{name: "no detail at all", detail: `not json`, want: nil},
		{name: "detail is a JSON array, not an object", detail: `[1,2,3]`, want: nil},
		{name: "object with no embedded key", detail: `{"name":"x"}`, want: nil},
		{name: "embedded key present but not an object", detail: `{"_benzeneHeaders":"not an object"}`, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := embeddedHeaders(json.RawMessage(tt.detail))
			if len(got) != len(tt.want) {
				t.Errorf("embeddedHeaders() = %v, want %v", got, tt.want)
			}
		})
	}
}
