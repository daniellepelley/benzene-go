package awslambda

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// TestHTTPHandler_SpecRouteCarriesTheQuery: a route mapped to benzene:spec dispatches the query's
// type and format as the {type?, format?} request (contract-document.md §7), from every event
// shape - so ?type= and ?format= reach contract.Middleware as they do through
// httpbinding.SpecHandler.
func TestHTTPHandler_SpecRouteCarriesTheQuery(t *testing.T) {
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
	handler := HTTPHandler(builder, []httpbinding.Route{{Method: http.MethodGet, Path: "/spec", Topic: benzene.NewTopic(contract.TopicID)}})

	cases := []struct {
		name, event string
		status      int
		request     string
	}{
		{"v2 raw query string", `{"rawPath":"/spec","rawQueryString":"type=openapi&format=json","requestContext":{"http":{"method":"GET","path":"/spec"}}}`,
			http.StatusNotImplemented, `{"format":"json","type":"openapi"}`},
		{"v2 malformed pair skipped", `{"rawPath":"/spec","rawQueryString":"type=benzene&%zz","requestContext":{"http":{"method":"GET","path":"/spec"}}}`,
			http.StatusOK, `{"type":"benzene"}`},
		{"v2 no query", `{"rawPath":"/spec","requestContext":{"http":{"method":"GET","path":"/spec"}}}`,
			http.StatusOK, `{}`},
		{"v1 parameters", `{"httpMethod":"GET","path":"/spec","queryStringParameters":{"type":"benzene","format":"json"}}`,
			http.StatusOK, `{"format":"json","type":"benzene"}`},
		{"v1 multi-value parameters, first value", `{"httpMethod":"GET","path":"/spec","multiValueQueryStringParameters":{"type":["openapi","benzene"]}}`,
			http.StatusNotImplemented, `{"type":"openapi"}`},
		{"ALB parameters arrive encoded", `{"httpMethod":"GET","path":"/spec","requestContext":{"elb":{"targetGroupArn":"arn"}},"queryStringParameters":{"type":"a%20b","format":"%zz"}}`,
			http.StatusNotImplemented, `{"format":"%zz","type":"a b"}`},
		{"ALB multi-value parameters", `{"httpMethod":"GET","path":"/spec","requestContext":{"elb":{"targetGroupArn":"arn"}},"multiValueQueryStringParameters":{"format":["json"]}}`,
			http.StatusOK, `{"format":"json"}`},
		{"a body wins", `{"rawPath":"/spec","rawQueryString":"type=openapi","requestContext":{"http":{"method":"GET","path":"/spec"}},"body":"{\"type\":\"benzene\"}"}`,
			http.StatusOK, `{"type":"benzene"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen = ""
			result, err := handler(context.Background(), json.RawMessage(c.event))
			if err != nil {
				t.Fatalf("handler() error = %v", err)
			}
			var resp struct {
				StatusCode int    `json:"statusCode"`
				Body       string `json:"body"`
			}
			if err := json.Unmarshal(result, &resp); err != nil {
				t.Fatalf("response %s: %v", result, err)
			}
			if resp.StatusCode != c.status || seen != c.request {
				t.Errorf("status %d, request %q; want %d, %q (body %s)", resp.StatusCode, seen, c.status, c.request, resp.Body)
			}
		})
	}
}
