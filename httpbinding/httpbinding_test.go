package httpbinding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

// registerVersioned registers a topic under both its unversioned form and version "2", each
// answering with a distinguishable greeting so a test can tell which handler ran.
func registerVersioned(t *testing.T, registry *benzene.Registry) {
	t.Helper()
	mustRegister := func(topic benzene.Topic, prefix string) {
		h := benzene.Handler[greetRequest, greetResponse](func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
			return benzene.Ok(greetResponse{Greeting: prefix + req.Name})
		})
		if err := benzene.Register(registry, topic, h); err != nil {
			t.Fatalf("Register(%v) error = %v", topic, err)
		}
	}
	mustRegister(benzene.NewTopic("greet"), "v1:")
	mustRegister(benzene.NewTopic("greet").WithVersion("2"), "v2:")
}

func doGreet(t *testing.T, handler http.Handler, path string, headers map[string]string) greetResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(`{"name":"World"}`))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload greetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, rec.Body.String())
	}
	return payload
}

func TestHandler_VersionRouteSegmentSelectsExactHandler(t *testing.T) {
	registry := benzene.NewRegistry()
	registerVersioned(t, registry)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
	handler := Handler(builder, []Route{{Method: "GET", Path: "/v{version}/greet", Topic: benzene.NewTopic("greet")}})

	got := doGreet(t, handler, "/v2/greet", nil)
	if got.Greeting != "v2:World" {
		t.Errorf("Greeting = %q, want %q (the {version} route segment should select the exact handler)", got.Greeting, "v2:World")
	}
}

func TestHandler_RouteWithNoVersionSegmentFallsBackToHeader(t *testing.T) {
	registry := benzene.NewRegistry()
	registerVersioned(t, registry)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
	handler := Handler(builder, []Route{{Method: "GET", Path: "/greet", Topic: benzene.NewTopic("greet")}})

	// The matched route declares no "version" parameter, so versioning.md §2.1's fallback list
	// applies: the router reads benzene-version off the request header instead.
	got := doGreet(t, handler, "/greet", map[string]string{"benzene-version": "2"})
	if got.Greeting != "v2:World" {
		t.Errorf("Greeting = %q, want %q (no route version segment should fall back to the header)", got.Greeting, "v2:World")
	}

	got = doGreet(t, handler, "/greet", nil)
	if got.Greeting != "v1:World" {
		t.Errorf("Greeting = %q, want %q (no version signalled at all should use the default handler)", got.Greeting, "v1:World")
	}
}

func TestHandler_VersionRouteSegmentWinsOverHeader(t *testing.T) {
	registry := benzene.NewRegistry()
	registerVersioned(t, registry)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
	handler := Handler(builder, []Route{{Method: "GET", Path: "/v{version}/greet", Topic: benzene.NewTopic("greet")}})

	// A stray benzene-version header on a version-routed request is ignored: the route segment
	// already resolved the version, and RouterMiddleware leaves an already-set version untouched.
	got := doGreet(t, handler, "/v2/greet", map[string]string{"benzene-version": "1"})
	if got.Greeting != "v2:World" {
		t.Errorf("Greeting = %q, want %q (the route segment should win over a conflicting header)", got.Greeting, "v2:World")
	}
}

func TestHandler_VersionRouteSegmentAlsoBecomesRouteHeader(t *testing.T) {
	registry := benzene.NewRegistry()
	var seenHeaders map[string]string
	h := benzene.Handler[greetRequest, greetResponse](func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
		return benzene.Ok(greetResponse{Greeting: "ok"})
	})
	if err := benzene.Register(registry, benzene.NewTopic("greet").WithVersion("2"), h); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	pipeline := benzene.NewPipeline(
		func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
			seenHeaders = ic.Headers
			return next(ctx)
		},
		benzene.RouterMiddleware(registry),
	)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: pipeline}
	handler := Handler(builder, []Route{{Method: "GET", Path: "/v{version}/greet", Topic: benzene.NewTopic("greet")}})

	doGreet(t, handler, "/v2/greet", nil)

	// The "{version}" segment is still an ordinary route parameter too - it becomes "route-version"
	// like any other captured segment, on top of setting the dispatched topic's version.
	if got := seenHeaders["route-version"]; got != "2" {
		t.Errorf(`Headers["route-version"] = %q, want "2"`, got)
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

func TestListenAddr(t *testing.T) {
	t.Run("uses the port the platform named", func(t *testing.T) {
		t.Setenv(PortEnvVar, "9000")
		if got := ListenAddr(); got != ":9000" {
			t.Errorf("ListenAddr() = %q, want %q", got, ":9000")
		}
	})

	t.Run("falls back to DefaultPort when the variable is unset", func(t *testing.T) {
		t.Setenv(PortEnvVar, "")
		if got := ListenAddr(); got != ":"+DefaultPort {
			t.Errorf("ListenAddr() = %q, want %q", got, ":"+DefaultPort)
		}
	})

	t.Run("is exactly the explicit form its doc names", func(t *testing.T) {
		t.Setenv(PortEnvVar, "7001")
		port := os.Getenv(PortEnvVar)
		if port == "" {
			port = DefaultPort
		}
		if got, want := ListenAddr(), ":"+port; got != want {
			t.Errorf("ListenAddr() = %q, explicit form gives %q - the shorthand must compose it", got, want)
		}
	})
}

// TestWriteNativeResponse_HTTPFailureCarriesTheHTTPStatusAndProblemContentType pins
// wire-contracts.md §4.1's two HTTP-only obligations on a problem body: the `status` member equal
// to the code actually sent (the transport-neutral document omits it, §1.3), and
// content-type: application/problem+json.
func TestWriteNativeResponse_HTTPFailureCarriesTheHTTPStatusAndProblemContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	failed := false
	writeNativeResponse(rec, wire.Response{
		StatusCode:   "not-found",
		Headers:      map[string]string{"content-type": "application/json"},
		Body:         `{"type":"https://benzene.app/problems/not-found","benzeneStatus":"not-found","detail":"missing"}`,
		IsSuccessful: &failed,
	})

	if rec.Code != 404 {
		t.Errorf("HTTP code = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("content-type"); got != "application/problem+json" {
		t.Errorf("content-type = %q, want application/problem+json", got)
	}
	var problem map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("body is not JSON: %v; body = %s", err, rec.Body.String())
	}
	if problem["status"] != float64(404) {
		t.Errorf("problem status = %v, want 404 equal to the code sent", problem["status"])
	}
	if problem["benzeneStatus"] != "not-found" {
		t.Errorf("benzeneStatus = %v, want not-found (unchanged)", problem["benzeneStatus"])
	}
}

// A success response is untouched: no status member injected, content-type left alone.
func TestWriteNativeResponse_SuccessIsUntouched(t *testing.T) {
	rec := httptest.NewRecorder()
	ok := true
	writeNativeResponse(rec, wire.Response{
		StatusCode:   "ok",
		Headers:      map[string]string{"content-type": "application/json"},
		Body:         `{"greeting":"hello"}`,
		IsSuccessful: &ok,
	})

	if rec.Code != 200 {
		t.Errorf("HTTP code = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q, want application/json for a success", got)
	}
	if rec.Body.String() != `{"greeting":"hello"}` {
		t.Errorf("body = %s, want it passed through unchanged", rec.Body.String())
	}
}
