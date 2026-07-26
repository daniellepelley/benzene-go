package cloudevents

import (
	"context"
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

func TestHandler_StructuredModeIsAcked(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	body := `{"specversion":"1.0","id":"1","source":"/eventgrid","type":"greet","data":{"name":"Cloud"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", StructuredContentType)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d; body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

func TestHandler_BinaryModeIsAcked(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(`{"name":"Binary"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ce-specversion", "1.0")
	req.Header.Set("ce-id", "1")
	req.Header.Set("ce-source", "/knative")
	req.Header.Set("ce-type", "greet")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d; body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

func TestHandler_FailedDispatchIsNackedWithErrorPayload(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	body := `{"specversion":"1.0","id":"1","source":"/s","type":"greet","data":{"name":""}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Content-Type", StructuredContentType)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

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

func TestHandler_InvalidDeliveries(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"structured but malformed", StructuredContentType, "{not valid", http.StatusBadRequest},
		{"structured but missing required attribute", StructuredContentType, `{"specversion":"1.0"}`, http.StatusBadRequest},
		{"batched mode", "application/cloudevents-batch+json", "[]", http.StatusUnsupportedMediaType},
		{"neither mode", "application/json", `{"name":"x"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := Handler(newTestBuilder(t))
			req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d; body = %s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestHandler_BodyReadErrorIsBadRequest(t *testing.T) {
	handler := Handler(newTestBuilder(t))

	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	req.Body = errReadCloser{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestParseBinary(t *testing.T) {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("ce-specversion", "1.0")
	header.Set("ce-id", "1")
	header.Set("ce-source", "/s")
	header.Set("ce-type", "greet")
	header.Set("ce-subject", "sub")
	header.Set("ce-time", "t")
	header.Set("ce-dataschema", "schema")
	header.Set("ce-traceparent", "00-abc-def-01")
	header.Set("ce-not-legal", "dropped")
	header.Set("x-unrelated", "dropped")

	event, err := ParseBinary(header, []byte(`{"name":"World"}`))
	if err != nil {
		t.Fatalf("ParseBinary() error = %v", err)
	}
	assertEventsEqual(t, event, Event{
		SpecVersion: "1.0", ID: "1", Source: "/s", Type: "greet",
		DataContentType: "application/json", Subject: "sub", Time: "t", DataSchema: "schema",
		Data:       json.RawMessage(`{"name":"World"}`),
		Extensions: map[string]string{"traceparent": "00-abc-def-01"},
	})
}

func TestParseBinary_EmptyBodyHasNoData(t *testing.T) {
	header := http.Header{}
	header.Set("ce-specversion", "1.0")
	header.Set("ce-id", "1")
	header.Set("ce-source", "/s")
	header.Set("ce-type", "greet")

	event, err := ParseBinary(header, nil)
	if err != nil {
		t.Fatalf("ParseBinary() error = %v", err)
	}
	if event.Data != nil {
		t.Errorf("Data = %s, want nil", event.Data)
	}
}

func TestParseBinary_MissingRequiredAttributeIsError(t *testing.T) {
	header := http.Header{}
	header.Set("ce-specversion", "1.0")

	if _, err := ParseBinary(header, nil); err == nil {
		t.Error("ParseBinary() error = nil, want an error for missing required attributes")
	}
}
