package httpbinding

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
	return benzene.Ok(greetResponse{Greeting: "Hello " + req.Name})
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

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
func (errReader) Close() error             { return nil }

func TestHandler_MatchedRouteReturnsNativeStatus(t *testing.T) {
	builder := newTestBuilder(t)
	handler := Handler(builder, []Route{{Method: "POST", Path: "/greet", Topic: benzene.NewTopic("greet")}})

	req := httptest.NewRequest(http.MethodPost, "/greet", strings.NewReader(`{"name":"World"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload greetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, rec.Body.String())
	}
	if payload.Greeting != "Hello World" {
		t.Errorf("Greeting = %q, want %q", payload.Greeting, "Hello World")
	}
	if ct := rec.Header().Get("content-type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestHandler_FailureStatusMapsToNativeHTTPCode(t *testing.T) {
	builder := newTestBuilder(t)
	handler := Handler(builder, []Route{{Method: "POST", Path: "/greet", Topic: benzene.NewTopic("greet")}})

	req := httptest.NewRequest(http.MethodPost, "/greet", strings.NewReader(`{"name":""}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_UnmatchedRouteIsNativeNotFound(t *testing.T) {
	builder := newTestBuilder(t)
	handler := Handler(builder, []Route{{Method: "POST", Path: "/greet", Topic: benzene.NewTopic("greet")}})

	req := httptest.NewRequest(http.MethodGet, "/no-such-route", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_BodyReadErrorIsBadRequest(t *testing.T) {
	builder := newTestBuilder(t)
	handler := Handler(builder, []Route{{Method: "POST", Path: "/greet", Topic: benzene.NewTopic("greet")}})

	req := httptest.NewRequest(http.MethodPost, "/greet", nil)
	req.Body = errReader{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_HeadersAreFlattenedLowercase(t *testing.T) {
	registry := benzene.NewRegistry()
	var seenHeaders map[string]string
	capture := benzene.Handler[greetRequest, greetResponse](func(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
		return benzene.Ok(greetResponse{Greeting: "ok"})
	})
	if err := benzene.Register(registry, benzene.NewTopic("greet"), capture); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	// Inspect headers via a middleware ahead of the router, since the handler itself has no
	// access to InvocationContext.Headers in this signature.
	pipeline := benzene.NewPipeline(
		func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
			seenHeaders = ic.Headers
			return next(ctx)
		},
		benzene.RouterMiddleware(registry),
	)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: pipeline}
	handler := Handler(builder, []Route{{Method: "POST", Path: "/greet", Topic: benzene.NewTopic("greet")}})

	req := httptest.NewRequest(http.MethodPost, "/greet", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("X-Correlation-Id", "abc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := seenHeaders["x-correlation-id"]; got != "abc" {
		t.Errorf(`Headers["x-correlation-id"] = %q, want "abc"`, got)
	}
}

func TestHeadersFrom_SkipsKeysWithNoValues(t *testing.T) {
	// http.Header.Set/Add always append a non-empty value; a key with a zero-length value
	// slice only arises from direct map manipulation (e.g. a proxy or middleware that deletes
	// a value but leaves the key). headersFrom must tolerate that without panicking.
	h := http.Header{}
	h["X-Empty"] = []string{}
	h.Set("X-Present", "value")

	flat := headersFrom(h)

	if _, ok := flat["x-empty"]; ok {
		t.Error(`flat["x-empty"] should be absent for a key with no values`)
	}
	if flat["x-present"] != "value" {
		t.Errorf(`flat["x-present"] = %q, want "value"`, flat["x-present"])
	}
}

func TestEnvelopeHandler_RoundTrip(t *testing.T) {
	builder := newTestBuilder(t)
	handler := EnvelopeHandler(builder)

	envReq, err := wire.MarshalRequest(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"World"}`})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(envReq)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("outer HTTP status = %d, want %d", rec.Code, http.StatusOK)
	}
	resp, err := wire.UnmarshalResponse(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("UnmarshalResponse() error = %v; body = %s", err, rec.Body.String())
	}
	if resp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("envelope StatusCode = %q, want %q", resp.StatusCode, benzene.StatusOk)
	}
}

func TestEnvelopeHandler_FailureStaysHTTP200(t *testing.T) {
	builder := newTestBuilder(t)
	handler := EnvelopeHandler(builder)

	envReq, err := wire.MarshalRequest(wire.Request{Topic: "no:such:topic", Headers: map[string]string{}, Body: ""})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(envReq)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("outer HTTP status = %d, want %d (the real outcome travels in the envelope)", rec.Code, http.StatusOK)
	}
	resp, err := wire.UnmarshalResponse(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("UnmarshalResponse() error = %v", err)
	}
	if resp.StatusCode != string(benzene.StatusNotFound) {
		t.Errorf("envelope StatusCode = %q, want %q", resp.StatusCode, benzene.StatusNotFound)
	}
}

func TestEnvelopeHandler_MalformedEnvelopeIsBadRequest(t *testing.T) {
	builder := newTestBuilder(t)
	handler := EnvelopeHandler(builder)

	req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader("{not valid json"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestEnvelopeHandler_BodyReadErrorIsBadRequest(t *testing.T) {
	builder := newTestBuilder(t)
	handler := EnvelopeHandler(builder)

	req := httptest.NewRequest(http.MethodPost, "/invoke", nil)
	req.Body = errReader{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
