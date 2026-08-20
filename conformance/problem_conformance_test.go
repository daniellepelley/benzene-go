package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/wire"
)

// problem-details-cases.json (wire-contracts.md §1.3, §3.1, §4.1) has three independent groups.
// `registry` and `envelopeCases` are required for the Benzene Core claim; `httpRules` is required
// for *each* HTTP binding a port ships - so it runs once per binding here (see httpBindings),
// not once for the port. Driving it through one binding only is what let two of the three ship
// without the `status` member the group exists to pin.
type problemDetailsFixture struct {
	Registry struct {
		Rows []struct {
			BenzeneStatus string `json:"benzeneStatus"`
			Type          string `json:"type"`
			HTTPStatus    int    `json:"httpStatus"`
		} `json:"rows"`
		UnknownStatus struct {
			HTTPStatus int `json:"httpStatus"`
		} `json:"unknownStatus"`
	} `json:"registry"`
	EnvelopeCases json.RawMessage `json:"envelopeCases"`
	HTTPRules     struct {
		FailureCases []struct {
			BenzeneStatus string `json:"benzeneStatus"`
			HTTPStatus    int    `json:"httpStatus"`
		} `json:"failureCases"`
		SuccessCase struct {
			BenzeneStatus string `json:"benzeneStatus"`
			HTTPStatus    int    `json:"httpStatus"`
			ContentType   string `json:"contentType"`
		} `json:"successCase"`
	} `json:"httpRules"`
}

func loadProblemFixture(t *testing.T) problemDetailsFixture {
	t.Helper()
	var fixture problemDetailsFixture
	loadFixture(t, "problem-details-cases.json", &fixture)
	return fixture
}

// TestConformance_ProblemRegistry compares this port's own §3.1 table against the fixture's rows
// directly - no message to build, the cheapest possible check.
func TestConformance_ProblemRegistry(t *testing.T) {
	fixture := loadProblemFixture(t)
	requireCases(t, len(fixture.Registry.Rows), "problem-details-cases", "registry.rows")

	for _, row := range fixture.Registry.Rows {
		t.Run(row.BenzeneStatus, func(t *testing.T) {
			if got := wire.ProblemType(row.BenzeneStatus); got != row.Type {
				t.Errorf("ProblemType(%q) = %q, want %q", row.BenzeneStatus, got, row.Type)
			}
			if got := wire.ProblemHTTPStatus(row.BenzeneStatus); got != row.HTTPStatus {
				t.Errorf("ProblemHTTPStatus(%q) = %d, want %d", row.BenzeneStatus, got, row.HTTPStatus)
			}
			// Title is never asserted for exact wording (conformance/README.md), only that the
			// registry has one - a row without a title is a row that fell out of the table.
			if wire.ProblemTitle(row.BenzeneStatus) == "" {
				t.Errorf("ProblemTitle(%q) is empty, want a registry title", row.BenzeneStatus)
			}
		})
	}
}

// TestConformance_ProblemRegistryUnknownStatus pins the fallback for an application-defined status:
// no registry row at all, and the §4.1 unknown-status HTTP code.
func TestConformance_ProblemRegistryUnknownStatus(t *testing.T) {
	fixture := loadProblemFixture(t)
	const appDefined = "insufficient-funds"

	if got := wire.ProblemType(appDefined); got != "" {
		t.Errorf("ProblemType(%q) = %q, want no registry type for an application-defined status", appDefined, got)
	}
	if got := wire.ProblemTitle(appDefined); got != "" {
		t.Errorf("ProblemTitle(%q) = %q, want no registry title for an application-defined status", appDefined, got)
	}
	if got := wire.ProblemHTTPStatus(appDefined); got != fixture.Registry.UnknownStatus.HTTPStatus {
		t.Errorf("ProblemHTTPStatus(%q) = %d, want %d", appDefined, got, fixture.Registry.UnknownStatus.HTTPStatus)
	}
}

// TestConformance_ProblemEnvelopeCases runs the envelopeCases group through the very same runner as
// envelope-cases.json, which is what the fixture format says they are.
func TestConformance_ProblemEnvelopeCases(t *testing.T) {
	fixture := loadProblemFixture(t)

	var cases envelopeCaseFixture
	if err := json.Unmarshal(fixture.EnvelopeCases, &cases.Cases); err != nil {
		t.Fatalf("problem-details-cases: parse envelopeCases: %v", err)
	}
	requireCases(t, len(cases.Cases), "problem-details-cases", "envelopeCases")

	runEnvelopeCases(t, cases)
}

// httpBinding is one shipped HTTP binding, reduced to what §4.1 is about: give it a request body
// for the canonical conformance:status handler, get back the response code, headers and body that
// binding actually puts on the wire.
type httpBinding struct {
	name   string
	invoke func(t *testing.T, body string) httpBindingResponse
}

// httpBindingResponse is a binding's response, normalized across the three native shapes (a
// net/http response, a Lambda proxy response envelope, an Azure Functions Outputs envelope).
// Header names are lower-cased, since only httpbinding's go through net/http's canonicalization.
type httpBindingResponse struct {
	statusCode int
	headers    map[string]string
	body       string
}

func (r httpBindingResponse) header(name string) string { return r.headers[strings.ToLower(name)] }

// httpBindings is every HTTP binding this port ships, and the §4.1 rules below run against all of
// them. A binding added here without being added to this list is a binding nothing holds to the
// wire contract.
//
// gcpfunctions is deliberately absent: RegisterHTTP is a pass-through to httpbinding.Handler with
// nothing of its own to get wrong, and it lives in its own module, which this one cannot import.
// The two awslambda entries are one binding with two event shapes; both assemble their own
// response envelope, so both are worth driving.
func httpBindings() []httpBinding {
	return []httpBinding{
		{name: "httpbinding", invoke: invokeOverHTTP},
		{name: "awslambda/apigateway-v2", invoke: invokeOverLambda(lambdaV2Event, lambdaProxyResponse)},
		{name: "awslambda/apigateway-v1", invoke: invokeOverLambda(lambdaV1Event, lambdaProxyResponse)},
		{name: "azurefunctions", invoke: invokeOverAzureFunctions},
	}
}

// TestConformance_ProblemHTTPRules is the §4.1 signalling rule: for an HTTP-bound failure the
// response line's code and the problem document's `status` member MUST come from the same mapping,
// so they can never disagree, and the content type says the body is a problem document.
func TestConformance_ProblemHTTPRules(t *testing.T) {
	fixture := loadProblemFixture(t)
	requireCases(t, len(fixture.HTTPRules.FailureCases), "problem-details-cases", "httpRules.failureCases")

	for _, binding := range httpBindings() {
		t.Run(binding.name, func(t *testing.T) {
			for _, c := range fixture.HTTPRules.FailureCases {
				t.Run(c.BenzeneStatus, func(t *testing.T) {
					resp := binding.invoke(t, `{"status":"`+c.BenzeneStatus+`","errors":["boom"]}`)

					if resp.statusCode != c.HTTPStatus {
						t.Errorf("response status = %d, want %d", resp.statusCode, c.HTTPStatus)
					}
					if got := resp.header("content-type"); got != "application/problem+json" {
						t.Errorf("content-type = %q, want application/problem+json", got)
					}

					var problem map[string]any
					if err := json.Unmarshal([]byte(resp.body), &problem); err != nil {
						t.Fatalf("body is not JSON: %v; body = %s", err, resp.body)
					}
					status, ok := problem["status"].(float64)
					if !ok {
						t.Fatalf("problem document has no numeric status member: %s", resp.body)
					}
					if int(status) != c.HTTPStatus {
						t.Errorf("problem status member = %d, want %d (it must equal the response status)", int(status), c.HTTPStatus)
					}
					if int(status) != resp.statusCode {
						t.Errorf("problem status member %d disagrees with the response status %d", int(status), resp.statusCode)
					}
					if problem["benzeneStatus"] != c.BenzeneStatus {
						t.Errorf("benzeneStatus = %v, want %q", problem["benzeneStatus"], c.BenzeneStatus)
					}
				})
			}
		})
	}
}

// TestConformance_ProblemHTTPSuccessUnaffected pins the other half: a success response carries no
// problem document and an ordinary content type.
func TestConformance_ProblemHTTPSuccessUnaffected(t *testing.T) {
	fixture := loadProblemFixture(t)
	success := fixture.HTTPRules.SuccessCase

	for _, binding := range httpBindings() {
		t.Run(binding.name, func(t *testing.T) {
			resp := binding.invoke(t, `{"status":"`+success.BenzeneStatus+`"}`)

			if resp.statusCode != success.HTTPStatus {
				t.Errorf("response status = %d, want %d", resp.statusCode, success.HTTPStatus)
			}
			if got := resp.header("content-type"); !strings.HasPrefix(got, success.ContentType) {
				t.Errorf("content-type = %q, want %q", got, success.ContentType)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(resp.body), &payload); err != nil {
				t.Fatalf("body is not JSON: %v; body = %s", err, resp.body)
			}
			for _, member := range []string{"type", "title", "status", "benzeneStatus", "errors"} {
				if _, present := payload[member]; present {
					t.Errorf("a success body must carry no problem member, found %q: %s", member, resp.body)
				}
			}
		})
	}
}

// conformanceBuilder is the canonical conformance:status handler wired into an application, the
// single service every binding below is asked to expose.
func conformanceBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := canonicalHandlerRegistry(t)
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

// conformanceRoutes is the one route each binding serves. The path is the same everywhere, which
// for azurefunctions also happens to be the local invocation path convention ("/<FunctionName>").
func conformanceRoutes() []httpbinding.Route {
	return []httpbinding.Route{
		{Method: http.MethodPost, Path: "/status", Topic: benzene.NewTopic("conformance:status")},
	}
}

// invokeOverHTTP drives the canonical handler through the real net/http binding.
//
// httpbinding.Handler, deliberately, not EnvelopeHandler: the envelope binding tunnels a wire
// response inside an HTTP 200, so its response line says nothing about the Benzene status. §4.1 is
// a rule about the HTTP binding's own response line and its own body agreeing, which only the
// route-based handler produces.
func invokeOverHTTP(t *testing.T, body string) httpBindingResponse {
	t.Helper()

	handler := httpbinding.Handler(conformanceBuilder(t), conformanceRoutes())

	req := httptest.NewRequest(http.MethodPost, "/status", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	t.Cleanup(func() { resp.Body.Close() })

	headers := make(map[string]string, len(resp.Header))
	for name := range resp.Header {
		headers[strings.ToLower(name)] = resp.Header.Get(name)
	}
	return httpBindingResponse{statusCode: resp.StatusCode, headers: headers, body: rec.Body.String()}
}

// lambdaV2Event is the API Gateway HTTP API v2.0 / Function URL request shape.
func lambdaV2Event(body string) string {
	encoded, _ := json.Marshal(body)
	return `{"rawPath":"/status","headers":{"content-type":"application/json"},` +
		`"requestContext":{"http":{"method":"POST","path":"/status"}},"body":` + string(encoded) + `}`
}

// lambdaV1Event is the API Gateway REST / HTTP API v1.0 / ALB request shape.
func lambdaV1Event(body string) string {
	encoded, _ := json.Marshal(body)
	return `{"httpMethod":"POST","path":"/status","headers":{"content-type":"application/json"},` +
		`"body":` + string(encoded) + `}`
}

// lambdaProxyResponse reads the proxy response envelope both Lambda shapes share for the fields
// §4.1 cares about.
func lambdaProxyResponse(t *testing.T, raw json.RawMessage) httpBindingResponse {
	t.Helper()
	var proxy struct {
		StatusCode int               `json:"statusCode"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}
	if err := json.Unmarshal(raw, &proxy); err != nil {
		t.Fatalf("lambda response is not a proxy response envelope: %v; response = %s", err, raw)
	}
	return httpBindingResponse{statusCode: proxy.StatusCode, headers: lowerKeys(proxy.Headers), body: proxy.Body}
}

// invokeOverLambda drives the canonical handler through awslambda.HTTPHandler for one event shape.
func invokeOverLambda(event func(string) string, read func(*testing.T, json.RawMessage) httpBindingResponse) func(*testing.T, string) httpBindingResponse {
	return func(t *testing.T, body string) httpBindingResponse {
		t.Helper()
		handler := awslambda.HTTPHandler(conformanceBuilder(t), conformanceRoutes())
		raw, err := handler(context.Background(), json.RawMessage(event(body)))
		if err != nil {
			t.Fatalf("awslambda handler returned an error: %v", err)
		}
		return read(t, raw)
	}
}

// invokeOverAzureFunctions drives the canonical handler through the Azure Functions custom-handler
// binding. The outer HTTP response is always 200 by that platform's contract - the invocation's
// real outcome is in Outputs.res, which is what §4.1 is about here.
func invokeOverAzureFunctions(t *testing.T, body string) httpBindingResponse {
	t.Helper()

	handler := azurefunctions.Handler(conformanceBuilder(t), conformanceRoutes())

	trigger, err := json.Marshal(map[string]any{
		"Data": map[string]any{
			"req": map[string]any{
				"Method":  http.MethodPost,
				"Headers": map[string]string{"content-type": "application/json"},
				"Body":    body,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal invocation payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/status", strings.NewReader(string(trigger)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("custom handler answered the Functions host with %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var inv struct {
		Outputs struct {
			Res struct {
				StatusCode string            `json:"statusCode"`
				Body       string            `json:"body"`
				Headers    map[string]string `json:"headers"`
			} `json:"res"`
		} `json:"Outputs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatalf("invocation response is not the custom-handler envelope: %v; response = %s", err, rec.Body.String())
	}
	code, err := strconv.Atoi(inv.Outputs.Res.StatusCode)
	if err != nil {
		t.Fatalf("Outputs.res.statusCode = %q, not an integer", inv.Outputs.Res.StatusCode)
	}
	return httpBindingResponse{statusCode: code, headers: lowerKeys(inv.Outputs.Res.Headers), body: inv.Outputs.Res.Body}
}

func lowerKeys(headers map[string]string) map[string]string {
	flat := make(map[string]string, len(headers))
	for name, value := range headers {
		flat[strings.ToLower(name)] = value
	}
	return flat
}
