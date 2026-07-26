package gcppubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func push(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/pubsub", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func pushEnvelope(t *testing.T, attributes map[string]string, data string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"data":       base64.StdEncoding.EncodeToString([]byte(data)),
			"attributes": attributes,
		},
		"subscription": "projects/p/subscriptions/s",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(body)
}

func TestHandler_TopicAttributeIsAcked(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	rec := push(t, handler, pushEnvelope(t, map[string]string{"topic": "greet"}, `{"name":"PubSub"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

func TestHandler_EnvelopeInDataIsAcked(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	message, err := json.Marshal(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"Envelope"}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	rec := push(t, handler, pushEnvelope(t, nil, string(message)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

func TestHandler_FailuresAreNacked(t *testing.T) {
	tests := []struct {
		name string
		body func(t *testing.T) string
		want int
	}{
		{
			name: "handler failure status",
			body: func(t *testing.T) string {
				return pushEnvelope(t, map[string]string{"topic": "greet"}, `{"name":""}`)
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "no topic resolvable",
			body: func(t *testing.T) string {
				return pushEnvelope(t, map[string]string{"other": "attr"}, "just some text")
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "malformed push envelope",
			body: func(t *testing.T) string { return "{not valid json" },
			want: http.StatusBadRequest,
		},
		{
			name: "malformed base64 data",
			body: func(t *testing.T) string {
				return `{"message":{"data":"not-valid-base64!!","attributes":{"topic":"greet"}}}`
			},
			want: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := Handler(newTestBuilder(t))
			rec := push(t, handler, tt.body(t))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestHandler_FailedDispatchBodyIsWireErrorPayload(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	rec := push(t, handler, pushEnvelope(t, map[string]string{"topic": "greet"}, `{"name":""}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var payload wire.ErrorPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal(error payload) error = %v; body = %s", err, rec.Body.String())
	}
	if payload.Status == "" {
		t.Errorf("error payload Status is empty; body = %s", rec.Body.String())
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestHandler_BodyReadErrorIsNacked(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	req := httptest.NewRequest(http.MethodPost, "/pubsub", nil)
	req.Body = errReadCloser{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestResolveRequest(t *testing.T) {
	tests := []struct {
		name        string
		message     pushMessage
		data        string
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name:        "topic attribute wins over envelope body, case-insensitively",
			message:     pushMessage{Attributes: map[string]string{"Topic": "greet", "x-correlation-id": "abc"}},
			data:        `{"topic":"other","headers":{},"body":"x"}`,
			wantTopic:   "greet",
			wantBody:    `{"topic":"other","headers":{},"body":"x"}`,
			wantHeaders: map[string]string{"x-correlation-id": "abc"},
		},
		{
			name:        "envelope headers merge with attribute headers",
			message:     pushMessage{Attributes: map[string]string{"from-attributes": "a"}},
			data:        `{"topic":"greet","headers":{"from-envelope":"e"},"body":"{}"}`,
			wantTopic:   "greet",
			wantBody:    `{}`,
			wantHeaders: map[string]string{"from-attributes": "a", "from-envelope": "e"},
		},
		{
			name:        "nothing resolvable yields empty topic and raw data",
			message:     pushMessage{},
			data:        "plain text",
			wantTopic:   "",
			wantBody:    "plain text",
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := resolveRequest(tt.message, tt.data)
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
