package conformance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/wire"
)

// problem-details-cases.json (wire-contracts.md §1.3, §3.1, §4.1) has three independent groups.
// `registry` and `envelopeCases` are required for the Benzene Core claim; `httpRules` is required
// for each HTTP binding a port ships, and this port ships httpbinding, so all three run here.
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

// TestConformance_ProblemHTTPRules is the §4.1 signalling rule: for an HTTP-bound failure the
// response line's code and the problem document's `status` member MUST come from the same mapping,
// so they can never disagree, and the content type says the body is a problem document.
func TestConformance_ProblemHTTPRules(t *testing.T) {
	fixture := loadProblemFixture(t)
	requireCases(t, len(fixture.HTTPRules.FailureCases), "problem-details-cases", "httpRules.failureCases")

	for _, c := range fixture.HTTPRules.FailureCases {
		t.Run(c.BenzeneStatus, func(t *testing.T) {
			resp, body := invokeOverHTTP(t, `{"status":"`+c.BenzeneStatus+`","errors":["boom"]}`)

			if resp.StatusCode != c.HTTPStatus {
				t.Errorf("response status = %d, want %d", resp.StatusCode, c.HTTPStatus)
			}
			if got := resp.Header.Get("content-type"); got != "application/problem+json" {
				t.Errorf("content-type = %q, want application/problem+json", got)
			}

			var problem map[string]any
			if err := json.Unmarshal([]byte(body), &problem); err != nil {
				t.Fatalf("body is not JSON: %v; body = %s", err, body)
			}
			status, ok := problem["status"].(float64)
			if !ok {
				t.Fatalf("problem document has no numeric status member: %s", body)
			}
			if int(status) != c.HTTPStatus {
				t.Errorf("problem status member = %d, want %d (it must equal the response status)", int(status), c.HTTPStatus)
			}
			if int(status) != resp.StatusCode {
				t.Errorf("problem status member %d disagrees with the response status %d", int(status), resp.StatusCode)
			}
			if problem["benzeneStatus"] != c.BenzeneStatus {
				t.Errorf("benzeneStatus = %v, want %q", problem["benzeneStatus"], c.BenzeneStatus)
			}
		})
	}
}

// TestConformance_ProblemHTTPSuccessUnaffected pins the other half: a success response carries no
// problem document and an ordinary content type.
func TestConformance_ProblemHTTPSuccessUnaffected(t *testing.T) {
	fixture := loadProblemFixture(t)
	success := fixture.HTTPRules.SuccessCase

	resp, body := invokeOverHTTP(t, `{"status":"`+success.BenzeneStatus+`"}`)

	if resp.StatusCode != success.HTTPStatus {
		t.Errorf("response status = %d, want %d", resp.StatusCode, success.HTTPStatus)
	}
	if got := resp.Header.Get("content-type"); !strings.HasPrefix(got, success.ContentType) {
		t.Errorf("content-type = %q, want %q", got, success.ContentType)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body is not JSON: %v; body = %s", err, body)
	}
	for _, member := range []string{"type", "title", "status", "benzeneStatus", "errors"} {
		if _, present := payload[member]; present {
			t.Errorf("a success body must carry no problem member, found %q: %s", member, body)
		}
	}
}

// invokeOverHTTP drives the canonical conformance:status handler through the real HTTP binding and
// returns the recorded response and its body.
//
// httpbinding.Handler, deliberately, not EnvelopeHandler: the envelope binding tunnels a wire
// response inside an HTTP 200, so its response line says nothing about the Benzene status. §4.1 is
// a rule about the HTTP binding's own response line and its own body agreeing, which only the
// route-based handler produces.
func invokeOverHTTP(t *testing.T, body string) (*http.Response, string) {
	t.Helper()

	registry := canonicalHandlerRegistry(t)
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
	handler := httpbinding.Handler(builder, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/status", Topic: benzene.NewTopic("conformance:status")},
	})

	req := httptest.NewRequest(http.MethodPost, "/status", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	t.Cleanup(func() { resp.Body.Close() })
	return resp, rec.Body.String()
}
