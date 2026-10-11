package httpbinding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
)

// specBuilder records the benzene:spec request each GET dispatches, and answers it as
// contract.Middleware would, behind an auth middleware - so the test sees that the request runs
// the pipeline, not a side door.
func specBuilder(t *testing.T, seen *string) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	doc, err := contract.Derive(registry, nil, contract.Info{Service: "spec-test"})
	if err != nil {
		t.Fatal(err)
	}
	auth := func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		if ic.Headers["authorization"] != "let-me-in" {
			ic.Result = benzene.Unauthorized[any]("no")
			return nil
		}
		raw, _ := ic.Request.(json.RawMessage)
		*seen = string(raw)
		return next(ctx)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(auth, contract.Middleware(doc), benzene.RouterMiddleware(registry)),
	}
}

func TestSpecHandler(t *testing.T) {
	var seen string
	handler := SpecHandler(specBuilder(t, &seen))

	cases := []struct {
		name, method, target, auth string
		status                     int
		request                    string
	}{
		{"default document", http.MethodGet, SpecPath, "let-me-in", http.StatusOK, `{}`},
		{"query copied", http.MethodGet, SpecPath + "?type=Benzene&format=json&other=x", "let-me-in", http.StatusOK, `{"format":"json","type":"Benzene"}`},
		{"unsupported type", http.MethodGet, SpecPath + "?type=openapi", "let-me-in", http.StatusNotImplemented, `{"type":"openapi"}`},
		{"empty type is the default", http.MethodGet, SpecPath + "?type=", "let-me-in", http.StatusOK, `{"type":""}`},
		{"head", http.MethodHead, SpecPath, "let-me-in", http.StatusOK, `{}`},
		{"pipeline auth applies", http.MethodGet, SpecPath, "", http.StatusUnauthorized, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen = ""
			req := httptest.NewRequest(c.method, c.target, nil)
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, c.status, rec.Body)
			}
			if seen != c.request {
				t.Errorf("dispatched request = %q, want %q", seen, c.request)
			}
			if c.status == http.StatusOK && c.method == http.MethodGet {
				var doc map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc["schemaDialect"] != contract.SchemaDialect {
					t.Errorf("body = %s", rec.Body)
				}
			}
		})
	}
}

func TestSpecHandler_RejectsOtherMethods(t *testing.T) {
	var seen string
	rec := httptest.NewRecorder()
	SpecHandler(specBuilder(t, &seen)).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, SpecPath, nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" || seen != "" {
		t.Errorf("status = %d, Allow = %q, dispatched = %q", rec.Code, rec.Header().Get("Allow"), seen)
	}
}

// TestHandler_SpecRouteCarriesTheQuery: a Route mapped to benzene:spec - the way a service mounts
// its self-description on a path of its own - dispatches the query's type and format as the
// request, like SpecHandler; a request that brings a body keeps it.
func TestHandler_SpecRouteCarriesTheQuery(t *testing.T) {
	var seen string
	handler := Handler(specBuilder(t, &seen), []Route{
		{Method: http.MethodGet, Path: "/spec", Topic: benzene.NewTopic(contract.TopicID)},
		{Method: http.MethodPost, Path: "/spec", Topic: benzene.NewTopic(contract.TopicID)},
	})

	cases := []struct {
		name, method, target, body string
		status                     int
		request                    string
	}{
		{"query copied", http.MethodGet, "/spec?type=benzene&format=json", "", http.StatusOK, `{"format":"json","type":"benzene"}`},
		{"unsupported type", http.MethodGet, "/spec?type=openapi", "", http.StatusNotImplemented, `{"type":"openapi"}`},
		{"no query is the default", http.MethodGet, "/spec", "", http.StatusOK, `{}`},
		{"a body wins", http.MethodPost, "/spec?type=openapi", `{"type":"benzene"}`, http.StatusOK, `{"type":"benzene"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen = ""
			req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
			req.Header.Set("Authorization", "let-me-in")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != c.status || seen != c.request {
				t.Errorf("status %d, request %q; want %d, %q (body %s)", rec.Code, seen, c.status, c.request, rec.Body)
			}
		})
	}
}

func TestRouteBody_LeavesOtherTopicsAlone(t *testing.T) {
	if got := RouteBody(benzene.NewTopic("order:create"), "", url.Values{"type": {"x"}}); got != "" {
		t.Errorf("RouteBody = %q, want the empty body untouched", got)
	}
}
