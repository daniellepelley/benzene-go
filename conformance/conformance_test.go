// Package conformance runs this port against the language-neutral fixtures vendored from
// daniellepelley/Benzene's docs/specification/conformance/ (see testdata/README.md for how
// they got here and how to re-sync them). Passing these fixtures is what "conformant" means
// per conformance/README.md - API shape is explicitly not part of conformance.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpstatus"
	"github.com/daniellepelley/benzene-go/wire"
)

// --- status-vocabulary.json ---

type statusVocabularyFixture struct {
	Statuses []struct {
		Status    string `json:"status"`
		IsSuccess bool   `json:"isSuccess"`
	} `json:"statuses"`
}

func TestConformance_StatusVocabulary(t *testing.T) {
	var fixture statusVocabularyFixture
	loadFixture(t, "status-vocabulary.json", &fixture)

	for _, entry := range fixture.Statuses {
		t.Run(entry.Status, func(t *testing.T) {
			if got := benzene.Status(entry.Status).IsSuccess(); got != entry.IsSuccess {
				t.Errorf("Status(%q).IsSuccess() = %v, want %v", entry.Status, got, entry.IsSuccess)
			}
		})
	}
}

// --- http-status-mapping.json ---

type mappingRow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type mappingFixture struct {
	Forward []mappingRow `json:"forward"`
	Reverse []mappingRow `json:"reverse"`
}

func TestConformance_HTTPStatusMapping(t *testing.T) {
	var fixture mappingFixture
	loadFixture(t, "http-status-mapping.json", &fixture)

	t.Run("forward", func(t *testing.T) {
		for _, row := range fixture.Forward {
			t.Run(row.From, func(t *testing.T) {
				status := benzene.Status(row.From)
				if row.From == "<unknown>" {
					status = benzene.Status("some-status-this-mapper-has-never-seen")
				}
				want, err := strconv.Atoi(row.To)
				if err != nil {
					t.Fatalf("fixture row %q has non-numeric \"to\" %q", row.From, row.To)
				}
				if got := httpstatus.ToHTTP(status); got != want {
					t.Errorf("ToHTTP(%q) = %d, want %d", row.From, got, want)
				}
			})
		}
	})

	t.Run("reverse", func(t *testing.T) {
		for _, row := range fixture.Reverse {
			t.Run(row.From, func(t *testing.T) {
				code, err := strconv.Atoi(row.From)
				if err != nil {
					t.Fatalf("fixture row has non-numeric \"from\" %q", row.From)
				}
				if got := httpstatus.FromHTTP(code); got != benzene.Status(row.To) {
					t.Errorf("FromHTTP(%d) = %q, want %q", code, got, row.To)
				}
			})
		}
	})
}

// --- envelope-cases.json ---
//
// Run against the canonical conformance handlers every runner MUST register natively
// (testdata/README.md / the upstream conformance/README.md's "Canonical handlers" section).

type conformanceGreetRequest struct {
	Name string `json:"name"`
}

type conformanceGreetResponse struct {
	Greeting string `json:"greeting"`
}

func conformanceGreetHandler(_ context.Context, req conformanceGreetRequest) benzene.Result[conformanceGreetResponse] {
	return benzene.Ok(conformanceGreetResponse{Greeting: "Hello " + req.Name})
}

type conformanceStatusRequest struct {
	Status string   `json:"status"`
	Errors []string `json:"errors,omitempty"`
}

type conformanceStatusResponse struct {
	Applied string `json:"applied"`
}

func conformanceStatusHandler(_ context.Context, req conformanceStatusRequest) benzene.Result[conformanceStatusResponse] {
	status := benzene.Status(req.Status)
	if status.IsSuccess() {
		return benzene.Result[conformanceStatusResponse]{Status: status, Payload: &conformanceStatusResponse{Applied: req.Status}}
	}
	return benzene.Fail[conformanceStatusResponse](status, req.Errors...)
}

type envelopeCaseFixture struct {
	Cases []struct {
		Name    string `json:"name"`
		Request struct {
			Topic   string            `json:"topic"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"request"`
		Expected struct {
			StatusCode string            `json:"statusCode"`
			Body       map[string]any    `json:"body,omitempty"`
			Headers    map[string]string `json:"headers,omitempty"`
		} `json:"expected"`
	} `json:"cases"`
}

func TestConformance_EnvelopeCases(t *testing.T) {
	var fixture envelopeCaseFixture
	loadFixture(t, "envelope-cases.json", &fixture)

	registry := benzene.NewRegistry()
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:greet"), benzene.Handler[conformanceGreetRequest, conformanceGreetResponse](conformanceGreetHandler)))
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:status"), benzene.Handler[conformanceStatusRequest, conformanceStatusResponse](conformanceStatusHandler)))
	container := benzene.NewContainer()
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			resp := envelope.Dispatch(context.Background(), pipeline, container, wire.Request{
				Topic:   c.Request.Topic,
				Headers: c.Request.Headers,
				Body:    c.Request.Body,
			})

			if resp.StatusCode != c.Expected.StatusCode {
				t.Errorf("statusCode = %q, want %q", resp.StatusCode, c.Expected.StatusCode)
			}

			if c.Expected.Body != nil {
				var actualBody map[string]any
				if resp.Body != "" {
					if err := json.Unmarshal([]byte(resp.Body), &actualBody); err != nil {
						t.Fatalf("response body is not valid JSON: %v; body = %s", err, resp.Body)
					}
				}
				for _, msg := range subsetMismatches(c.Expected.Body, actualBody) {
					t.Errorf("body %s", msg)
				}
			}

			if c.Expected.Headers != nil {
				actualHeaders := lowercaseKeys(resp.Headers)
				for key, want := range c.Expected.Headers {
					got, ok := actualHeaders[key]
					if !ok {
						t.Errorf("headers: missing key %q", key)
						continue
					}
					if got != want {
						t.Errorf("headers[%q] = %q, want %q", key, got, want)
					}
				}
			}
		})
	}
}

// subsetMismatches reports every key in expected that is absent from, or not deeply equal in,
// actual - nested objects are compared recursively. Extra keys in actual are ignored, per
// testdata/README.md's subset-matching rule.
func subsetMismatches(expected, actual map[string]any) []string {
	var mismatches []string
	for key, expectedValue := range expected {
		actualValue, ok := actual[key]
		if !ok {
			mismatches = append(mismatches, fmt.Sprintf("missing key %q", key))
			continue
		}
		if expectedMap, ok := expectedValue.(map[string]any); ok {
			actualMap, ok := actualValue.(map[string]any)
			if !ok {
				mismatches = append(mismatches, fmt.Sprintf("key %q: expected an object, got %T", key, actualValue))
				continue
			}
			mismatches = append(mismatches, subsetMismatches(expectedMap, actualMap)...)
			continue
		}
		if !reflect.DeepEqual(expectedValue, actualValue) {
			mismatches = append(mismatches, fmt.Sprintf("key %q: expected %v, got %v", key, expectedValue, actualValue))
		}
	}
	return mismatches
}

func lowercaseKeys(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

func loadFixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", name, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", name, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup error = %v", err)
	}
}
