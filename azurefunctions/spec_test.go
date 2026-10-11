package azurefunctions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// TestHandler_SpecRouteCarriesTheQuery: a Route mapped to benzene:spec (the /Spec function every
// azure-functions-mesh service mounts) dispatches the query's type and format as the {type?,
// format?} request (contract-document.md §7) - read from the trigger's Query object, either value
// shape, or else from its Url - as httpbinding.SpecHandler does.
func TestHandler_SpecRouteCarriesTheQuery(t *testing.T) {
	registry := benzene.NewRegistry()
	doc, err := contract.Derive(registry, nil, contract.Info{Service: "spec-test"})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	record := func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		raw, _ := ic.Request.(json.RawMessage)
		seen = string(raw)
		return next(ctx)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(record, contract.Middleware(doc), benzene.RouterMiddleware(registry)),
	}
	handler := Handler(builder, []httpbinding.Route{{Method: http.MethodGet, Path: "/Spec", Topic: benzene.NewTopic(contract.TopicID)}})

	cases := []struct {
		name, req string
		status    string
		request   string
	}{
		{"Query object", `{"Method":"GET","Url":"https://x/api/spec","Query":{"type":"openapi","format":"json"}}`, "501", `{"format":"json","type":"openapi"}`},
		{"Query lists", `{"Method":"GET","Query":{"type":["benzene"]}}`, "200", `{"type":"benzene"}`},
		{"Url when Query is empty", `{"Method":"GET","Url":"https://x/api/spec?type=openapi","Query":{}}`, "501", `{"type":"openapi"}`},
		{"Url when Query is absent", `{"Method":"GET","Url":"https://x/api/spec?format=json"}`, "200", `{"format":"json"}`},
		{"an unparseable Url is no query", `{"Method":"GET","Url":"://bad"}`, "200", `{}`},
		{"a body wins", `{"Method":"GET","Url":"https://x/api/spec?type=openapi","Body":"{\"type\":\"benzene\"}"}`, "200", `{"type":"benzene"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen = ""
			payload := `{"Data":{"req":` + c.req + `},"Metadata":{}}`
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/Spec", strings.NewReader(payload)))
			var inv invocationResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
				t.Fatalf("response %s: %v", rec.Body, err)
			}
			if res := inv.Outputs["res"]; res.StatusCode != c.status || seen != c.request {
				t.Errorf("status %s, request %q; want %s, %q (body %s)", res.StatusCode, seen, c.status, c.request, res.Body)
			}
		})
	}
}
