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
	"github.com/daniellepelley/benzene-go/grpcstatus"
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
	requireCases(t, len(fixture.Statuses), "status-vocabulary", "statuses")

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
	// IsSuccessful distinguishes the two "<unknown>" forward rows: an application-defined status
	// on a failed result vs one on a result explicitly marked successful. Absent (nil) on every
	// known-status row, where it has no effect.
	IsSuccessful *bool `json:"isSuccessful"`
}

type mappingFixture struct {
	Forward []mappingRow `json:"forward"`
	Reverse []mappingRow `json:"reverse"`
}

func TestConformance_HTTPStatusMapping(t *testing.T) {
	var fixture mappingFixture
	loadFixture(t, "http-status-mapping.json", &fixture)
	requireCases(t, len(fixture.Forward), "http-status-mapping", "forward")
	requireCases(t, len(fixture.Reverse), "http-status-mapping", "reverse")

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
				if got := httpstatus.ToHTTP(status, successFlags(row)...); got != want {
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

// --- grpc-status-mapping.json ---

// grpcCodeByName is the name<->number table for the gRPC status codes the fixture's
// "from"/"to" fields name as strings (https://github.com/grpc/grpc/blob/master/doc/statuscodes.md) -
// grpcstatus itself works in raw numeric codes (see its own package doc for why), so this
// table exists only to translate the fixture into calls this test can make.
var grpcCodeByName = map[string]int{
	"OK": 0, "Cancelled": 1, "Unknown": 2, "InvalidArgument": 3, "DeadlineExceeded": 4,
	"NotFound": 5, "AlreadyExists": 6, "PermissionDenied": 7, "ResourceExhausted": 8,
	"FailedPrecondition": 9, "Aborted": 10, "OutOfRange": 11, "Unimplemented": 12,
	"Internal": 13, "Unavailable": 14, "DataLoss": 15, "Unauthenticated": 16,
}

func TestConformance_GRPCStatusMapping(t *testing.T) {
	var fixture mappingFixture
	loadFixture(t, "grpc-status-mapping.json", &fixture)
	requireCases(t, len(fixture.Forward), "grpc-status-mapping", "forward")
	requireCases(t, len(fixture.Reverse), "grpc-status-mapping", "reverse")

	t.Run("forward", func(t *testing.T) {
		for _, row := range fixture.Forward {
			t.Run(row.From, func(t *testing.T) {
				status := benzene.Status(row.From)
				if row.From == "<unknown>" {
					status = benzene.Status("some-status-this-mapper-has-never-seen")
				}
				want, ok := grpcCodeByName[row.To]
				if !ok {
					t.Fatalf("fixture row %q has unrecognized gRPC code name %q", row.From, row.To)
				}
				if got := grpcstatus.ToGRPC(status, successFlags(row)...); got != want {
					t.Errorf("ToGRPC(%q) = %d, want %d (%s)", row.From, got, want, row.To)
				}
			})
		}
	})

	t.Run("reverse", func(t *testing.T) {
		for _, row := range fixture.Reverse {
			t.Run(row.From, func(t *testing.T) {
				code, ok := grpcCodeByName[row.From]
				if !ok {
					t.Fatalf("fixture row has unrecognized gRPC code name %q", row.From)
				}
				if got := grpcstatus.FromGRPC(code); got != benzene.Status(row.To) {
					t.Errorf("FromGRPC(%d) = %q, want %q (from %s)", code, got, row.To, row.From)
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

// conformanceProblemRequest / conformanceProblemHandler are the canonical `conformance:problem`
// handler (conformance/README.md "Canonical handlers"): always a validation-error carrying exactly
// one structured error built from the request's message/field/code.
//
// When appType is given, the emitted problem document's `type` is that value verbatim instead of the
// registry URI - the application-authored-problem case (wire-contracts.md §1.3). benzeneStatus is
// still validation-error and errors still carries the one structured error either way.
type conformanceProblemRequest struct {
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code,omitempty"`
	AppType string `json:"appType,omitempty"`
}

func conformanceProblemHandler(_ context.Context, req conformanceProblemRequest) benzene.Result[struct{}] {
	problemError := benzene.Error{Message: req.Message, Field: req.Field, Code: req.Code}

	if req.AppType != "" {
		return benzene.ProblemResult[struct{}](benzene.Problem{
			Type:          req.AppType,
			BenzeneStatus: string(benzene.StatusValidationError),
			Errors:        []benzene.Error{problemError},
		})
	}

	return benzene.ValidationErrorWith[struct{}](problemError)
}

// canonicalHandlerRegistry registers exactly the handlers conformance/README.md's "Canonical
// handlers" table names, and nothing else - cases targeting any other topic are asserting the
// router's not-found behavior, so an extra registration here would quietly weaken them.
func canonicalHandlerRegistry(t *testing.T) *benzene.Registry {
	t.Helper()
	registry := benzene.NewRegistry()
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:greet"), benzene.Handler[conformanceGreetRequest, conformanceGreetResponse](conformanceGreetHandler)))
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:status"), benzene.Handler[conformanceStatusRequest, conformanceStatusResponse](conformanceStatusHandler)))
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:problem"), benzene.Handler[conformanceProblemRequest, struct{}](conformanceProblemHandler)))
	return registry
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
			// IsSuccessful, when the fixture states it, is checked exactly against the response
			// envelope's own member (§1.2) - not inferred from the status.
			IsSuccessful *bool `json:"isSuccessful,omitempty"`
			// BodyExclude names members that MUST NOT appear in the parsed body. It is how the
			// fixtures pin the withdrawal of the old `status`-as-a-string member (§1.3): asserting
			// the new members are present would otherwise pass even for a writer that also still
			// emits the old one.
			BodyExclude []string `json:"bodyExclude,omitempty"`
		} `json:"expected"`
	} `json:"cases"`
}

func TestConformance_EnvelopeCases(t *testing.T) {
	var fixture envelopeCaseFixture
	loadFixture(t, "envelope-cases.json", &fixture)
	requireCases(t, len(fixture.Cases), "envelope-cases", "cases")
	runEnvelopeCases(t, fixture)
}

// runEnvelopeCases is the envelope case format (conformance/README.md) - shared, because
// problem-details-cases.json's envelopeCases group is defined as being in "exactly the envelope case
// format above". Running it through a second, similar-looking loop is how the two drift.
func runEnvelopeCases(t *testing.T, fixture envelopeCaseFixture) {
	t.Helper()

	registry := canonicalHandlerRegistry(t)
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
				for _, member := range c.Expected.BodyExclude {
					if _, present := actualBody[member]; present {
						t.Errorf("body must not contain %q: %s", member, resp.Body)
					}
				}
			}

			if c.Expected.IsSuccessful != nil {
				if resp.IsSuccessful == nil {
					t.Errorf("isSuccessful is absent, want %v stated outright (§1.2)", *c.Expected.IsSuccessful)
				} else if *resp.IsSuccessful != *c.Expected.IsSuccessful {
					t.Errorf("isSuccessful = %v, want %v", *resp.IsSuccessful, *c.Expected.IsSuccessful)
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

// requireCases fails the test when a fixture list the runner is about to iterate is empty.
//
// json.Unmarshal leaves a slice nil when the fixture has no such key, and `for range` over nil
// iterates nothing - so a renamed key upstream does not break the test, it disables it, and a run
// that checked nothing is indistinguishable from a clean pass. These fixtures are vendored
// snapshots of a canonical set that other people rename; the runner is the only thing positioned to
// notice the drift. (It has happened: the descriptor runner spent the whole producer/consumer role
// inversion reading a hash-property key the fixture had renamed, asserting nothing, staying green.)
func requireCases(t *testing.T, n int, fixture, key string) {
	t.Helper()
	if n == 0 {
		t.Fatalf("%s: fixture list %q is empty - the runner and the fixture have drifted", fixture, key)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup error = %v", err)
	}
}

// successFlags turns a fixture row's optional isSuccessful into the variadic argument the forward
// mappers take: nothing when the row does not carry one.
func successFlags(row mappingRow) []bool {
	if row.IsSuccessful == nil {
		return nil
	}
	return []bool{*row.IsSuccessful}
}
