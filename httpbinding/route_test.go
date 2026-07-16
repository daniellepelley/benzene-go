package httpbinding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestRouteTable_Match(t *testing.T) {
	table := NewRouteTable([]Route{
		{Method: "POST", Path: "/greet", Topic: benzene.NewTopic("exact")},
		{Method: "GET", Path: "/users/{id}", Topic: benzene.NewTopic("user")},
		{Method: "GET", Path: "/users/{id}/orders/{OrderID}", Topic: benzene.NewTopic("order")},
		{Method: "GET", Path: "/users/me", Topic: benzene.NewTopic("me")},
		{Method: "GET", Path: "/files/{}", Topic: benzene.NewTopic("literal-braces")},
	})

	tests := []struct {
		name       string
		method     string
		path       string
		wantTopic  string
		wantParams map[string]string
		wantOK     bool
	}{
		{name: "exact match", method: "POST", path: "/greet", wantTopic: "exact", wantOK: true},
		{name: "single parameter", method: "GET", path: "/users/42", wantTopic: "user", wantParams: map[string]string{"id": "42"}, wantOK: true},
		{
			name: "multiple parameters, names lower-cased", method: "GET", path: "/users/42/orders/oid-7",
			wantTopic: "order", wantParams: map[string]string{"id": "42", "orderid": "oid-7"}, wantOK: true,
		},
		{name: "exact wins over templated", method: "GET", path: "/users/me", wantTopic: "me", wantOK: true},
		{name: "method must match", method: "DELETE", path: "/users/42", wantOK: false},
		{name: "segment count must match", method: "GET", path: "/users/42/extra", wantOK: false},
		{name: "empty segment does not match a parameter", method: "GET", path: "/users//orders/x", wantOK: false},
		{name: "empty braces are a literal", method: "GET", path: "/files/{}", wantTopic: "literal-braces", wantParams: nil, wantOK: true},
		{name: "empty braces literal rejects other segments", method: "GET", path: "/files/anything", wantOK: false},
		{name: "no route at all", method: "GET", path: "/nope", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topic, params, ok := table.Match(tt.method, tt.path)
			if ok != tt.wantOK {
				t.Fatalf("Match() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if topic.String() != tt.wantTopic {
				t.Errorf("topic = %q, want %q", topic, tt.wantTopic)
			}
			if len(params) != len(tt.wantParams) {
				t.Fatalf("params = %v, want %v", params, tt.wantParams)
			}
			for k, v := range tt.wantParams {
				if params[k] != v {
					t.Errorf("params[%q] = %q, want %q", k, params[k], v)
				}
			}
		})
	}
}

func TestHandler_RouteParametersBecomeHeaders(t *testing.T) {
	registry := benzene.NewRegistry()
	echo := func(ctx context.Context, _ struct{}) benzene.Result[string] { return benzene.Ok("ok") }
	if err := benzene.Register(registry, benzene.NewTopic("user"), benzene.Handler[struct{}, string](echo)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	var seen map[string]string
	capture := benzene.Middleware(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		seen = ic.Headers
		return next(ctx)
	})
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(capture, benzene.RouterMiddleware(registry)),
	}
	handler := Handler(builder, []Route{{Method: "GET", Path: "/users/{id}", Topic: benzene.NewTopic("user")}})

	// The client tries to spoof the parameter with a literal route-id header - the captured
	// path segment must win, because the binding writes parameters after flattening.
	req := httptest.NewRequest(http.MethodGet, "/users/42", strings.NewReader(`{}`))
	req.Header.Set("route-id", "spoofed")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if seen["route-id"] != "42" {
		t.Errorf(`Headers["route-id"] = %q, want %q (path segment, not the spoofed header)`, seen["route-id"], "42")
	}
}

func TestHandler_HandlerSetResponseHeadersReachTheHTTPResponse(t *testing.T) {
	registry := benzene.NewRegistry()
	handler := func(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
		benzene.SetResponseHeader(ctx, "X-Request-Id", "abc-123")
		return benzene.Ok(greetResponse{Greeting: "Hello " + req.Name})
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](handler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
	h := Handler(builder, []Route{{Method: "POST", Path: "/greet", Topic: benzene.NewTopic("greet")}})

	req := httptest.NewRequest(http.MethodPost, "/greet", strings.NewReader(`{"name":"World"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("x-request-id"); got != "abc-123" {
		t.Errorf(`response header "x-request-id" = %q, want %q`, got, "abc-123")
	}
}
