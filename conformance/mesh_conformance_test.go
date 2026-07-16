package conformance

// Runners for the mesh fixtures (the main repo's docs/specification/mesh.md §7), vendored
// like every other fixture - see README.md. They exercise the mesh and meshd packages
// through the same envelope-dispatch surface a real caller uses. The mesh fixtures add one
// matching rule to the envelope cases' subset matching: arrays compare by exact length with
// per-element subset matching, and an expected empty array matches an absent-or-empty
// actual one (meshSubsetMismatches).

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
	"github.com/daniellepelley/benzene-go/wire"
)

// canonicalRegistry registers the two canonical conformance handlers (README.md), the set
// mesh-descriptor-cases.json pins schema derivation against.
func canonicalRegistry(t *testing.T) *benzene.Registry {
	t.Helper()
	registry := benzene.NewRegistry()
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:greet"), benzene.Handler[conformanceGreetRequest, conformanceGreetResponse](conformanceGreetHandler)))
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:status"), benzene.Handler[conformanceStatusRequest, conformanceStatusResponse](conformanceStatusHandler)))
	return registry
}

// --- mesh-descriptor-cases.json ---

type meshDescriptorFixture struct {
	ServiceInfo struct {
		Service        string `json:"service"`
		ServiceVersion string `json:"serviceVersion"`
		Placement      struct {
			Cloud  string `json:"cloud"`
			Region string `json:"region"`
		} `json:"placement"`
	} `json:"serviceInfo"`
	ExpectedDescriptor map[string]any `json:"expectedDescriptor"`
	Hash               struct {
		Prefix                    string `json:"prefix"`
		HexLength                 int    `json:"hexLength"`
		InvariantToInstanceID     bool   `json:"invariantToInstanceId"`
		SensitiveToServiceVersion bool   `json:"sensitiveToServiceVersion"`
		SensitiveToTopics         bool   `json:"sensitiveToTopics"`
	} `json:"hash"`
}

func TestConformance_MeshDescriptorCases(t *testing.T) {
	var fixture meshDescriptorFixture
	loadFixture(t, "mesh-descriptor-cases.json", &fixture)

	info := mesh.ServiceInfo{
		Service:        fixture.ServiceInfo.Service,
		ServiceVersion: fixture.ServiceInfo.ServiceVersion,
		Placement:      mesh.Placement{Cloud: fixture.ServiceInfo.Placement.Cloud, Region: fixture.ServiceInfo.Placement.Region},
	}
	descriptor := mesh.Describe(canonicalRegistry(t), info)

	t.Run("expected-descriptor", func(t *testing.T) {
		data, err := json.Marshal(descriptor)
		if err != nil {
			t.Fatalf("marshal descriptor: %v", err)
		}
		var actual map[string]any
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("unmarshal descriptor: %v", err)
		}
		for _, msg := range meshSubsetMismatches("descriptor", fixture.ExpectedDescriptor, actual) {
			t.Error(msg)
		}
	})

	t.Run("hash-format", func(t *testing.T) {
		hash := descriptor.DescriptorHash
		if !strings.HasPrefix(hash, fixture.Hash.Prefix) || len(hash) != len(fixture.Hash.Prefix)+fixture.Hash.HexLength {
			t.Fatalf("descriptorHash = %q, want %q + %d hex chars", hash, fixture.Hash.Prefix, fixture.Hash.HexLength)
		}
		if _, err := hex.DecodeString(hash[len(fixture.Hash.Prefix):]); err != nil {
			t.Errorf("descriptorHash suffix is not hex: %v", err)
		}
	})

	t.Run("hash-invariant-to-instance-id", func(t *testing.T) {
		if !fixture.Hash.InvariantToInstanceID {
			t.Skip("not asserted by the fixture")
		}
		other := info
		other.InstanceID = "some-other-instance"
		if got := mesh.Describe(canonicalRegistry(t), other).DescriptorHash; got != descriptor.DescriptorHash {
			t.Errorf("hash changed with instanceId: %q vs %q", got, descriptor.DescriptorHash)
		}
	})

	t.Run("hash-sensitive-to-service-version", func(t *testing.T) {
		if !fixture.Hash.SensitiveToServiceVersion {
			t.Skip("not asserted by the fixture")
		}
		bumped := info
		bumped.ServiceVersion = info.ServiceVersion + "-changed"
		if got := mesh.Describe(canonicalRegistry(t), bumped).DescriptorHash; got == descriptor.DescriptorHash {
			t.Errorf("hash did not change with serviceVersion: %q", got)
		}
	})

	t.Run("hash-sensitive-to-topics", func(t *testing.T) {
		if !fixture.Hash.SensitiveToTopics {
			t.Skip("not asserted by the fixture")
		}
		grown := canonicalRegistry(t)
		must(t, benzene.Register(grown, benzene.NewTopic("conformance:extra"), benzene.Handler[conformanceGreetRequest, conformanceGreetResponse](conformanceGreetHandler)))
		if got := mesh.Describe(grown, info).DescriptorHash; got == descriptor.DescriptorHash {
			t.Errorf("hash did not change with the topic set: %q", got)
		}
	})
}

// --- mesh-trace-cases.json ---

type meshTraceFixture struct {
	Traceparent []struct {
		Name         string `json:"name"`
		Header       string `json:"header"`
		Valid        bool   `json:"valid"`
		TraceID      string `json:"traceId"`
		ParentSpanID string `json:"parentSpanId"`
	} `json:"traceparent"`
	Invocations []struct {
		Name    string `json:"name"`
		Request struct {
			Topic   string            `json:"topic"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"request"`
		ExpectedEvent map[string]any `json:"expectedEvent"`
	} `json:"invocations"`
}

// captureExporter records exported events; the runner's stand-in for a trace feed.
type captureExporter struct {
	mu     sync.Mutex
	events []mesh.TraceEvent
}

func (c *captureExporter) Export(_ context.Context, event mesh.TraceEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

// conformancePanicHandler is the extra canonical mesh handler (conformance/README.md's mesh
// fixture formats): it panics unconditionally, pinning the rule that a handler panic is
// traced as ServiceUnavailable rather than lost.
func conformancePanicHandler(_ context.Context, _ json.RawMessage) benzene.Result[struct{}] {
	panic("conformance:panic always panics")
}

// dispatchTraced runs one envelope through a traced pipeline with the canonical handlers
// plus conformance:panic registered, returning the single exported event.
func dispatchTraced(t *testing.T, request wire.Request) mesh.TraceEvent {
	t.Helper()
	registry := canonicalRegistry(t)
	must(t, benzene.Register(registry, benzene.NewTopic("conformance:panic"), benzene.Handler[json.RawMessage, struct{}](conformancePanicHandler)))

	exporter := &captureExporter{}
	pipeline := benzene.NewPipeline(mesh.TraceMiddleware(mesh.ServiceInfo{Service: "conformance"}, exporter), benzene.RouterMiddleware(registry))
	envelope.Dispatch(context.Background(), pipeline, benzene.NewContainer(), request)

	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if len(exporter.events) != 1 {
		t.Fatalf("exported %d events, want exactly 1: %v", len(exporter.events), exporter.events)
	}
	return exporter.events[0]
}

func TestConformance_MeshTraceCases(t *testing.T) {
	var fixture meshTraceFixture
	loadFixture(t, "mesh-trace-cases.json", &fixture)

	t.Run("traceparent", func(t *testing.T) {
		for _, row := range fixture.Traceparent {
			t.Run(row.Name, func(t *testing.T) {
				headers := map[string]string{}
				if row.Header != "" {
					headers["traceparent"] = row.Header
				}
				event := dispatchTraced(t, wire.Request{Topic: "conformance:greet", Headers: headers, Body: `{"name":"world"}`})

				if row.Valid {
					if event.TraceID != row.TraceID {
						t.Errorf("traceId = %q, want the adopted %q", event.TraceID, row.TraceID)
					}
					if event.ParentSpanID != row.ParentSpanID {
						t.Errorf("parentSpanId = %q, want %q", event.ParentSpanID, row.ParentSpanID)
					}
					return
				}
				if event.ParentSpanID != "" {
					t.Errorf("parentSpanId = %q, want none for an invalid header", event.ParentSpanID)
				}
				if len(event.TraceID) != 32 {
					t.Fatalf("traceId = %q, want a fresh 32-hex-char id", event.TraceID)
				}
				if _, err := hex.DecodeString(event.TraceID); err != nil {
					t.Errorf("traceId %q is not hex: %v", event.TraceID, err)
				}
				if parts := strings.Split(row.Header, "-"); len(parts) > 1 && event.TraceID == parts[1] {
					t.Errorf("traceId adopted %q from an invalid header", parts[1])
				}
			})
		}
	})

	t.Run("invocations", func(t *testing.T) {
		for _, c := range fixture.Invocations {
			t.Run(c.Name, func(t *testing.T) {
				event := dispatchTraced(t, wire.Request{Topic: c.Request.Topic, Headers: c.Request.Headers, Body: c.Request.Body})

				data, err := json.Marshal(event)
				if err != nil {
					t.Fatalf("marshal event: %v", err)
				}
				var actual map[string]any
				if err := json.Unmarshal(data, &actual); err != nil {
					t.Fatalf("unmarshal event: %v", err)
				}
				for _, msg := range meshSubsetMismatches("event", c.ExpectedEvent, actual) {
					t.Error(msg)
				}
			})
		}
	})
}

// --- mesh-collector-cases.json ---

type meshCollectorFixture struct {
	Cases []struct {
		Name  string `json:"name"`
		Steps []struct {
			Request struct {
				Topic   string            `json:"topic"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"request"`
			Expected struct {
				StatusCode string         `json:"statusCode"`
				Body       map[string]any `json:"body,omitempty"`
			} `json:"expected"`
		} `json:"steps"`
	} `json:"cases"`
}

func TestConformance_MeshCollectorCases(t *testing.T) {
	var fixture meshCollectorFixture
	loadFixture(t, "mesh-collector-cases.json", &fixture)

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			builder := meshd.New(meshd.Options{}).Builder()

			for i, step := range c.Steps {
				resp := envelope.Dispatch(context.Background(), builder.Pipeline, builder.Container, wire.Request{
					Topic:   step.Request.Topic,
					Headers: step.Request.Headers,
					Body:    step.Request.Body,
				})

				if resp.StatusCode != step.Expected.StatusCode {
					t.Fatalf("step %d (%s): statusCode = %q, want %q (body %s)", i, step.Request.Topic, resp.StatusCode, step.Expected.StatusCode, resp.Body)
				}
				if step.Expected.Body == nil {
					continue
				}
				var actual map[string]any
				if resp.Body != "" {
					if err := json.Unmarshal([]byte(resp.Body), &actual); err != nil {
						t.Fatalf("step %d: response body is not valid JSON: %v; body = %s", i, err, resp.Body)
					}
				}
				for _, msg := range meshSubsetMismatches(fmt.Sprintf("step %d body", i), step.Expected.Body, actual) {
					t.Error(msg)
				}
			}
		})
	}
}

// meshSubsetMismatches implements the mesh fixtures' matching rule (the upstream
// conformance README's "Mesh fixture formats"): objects match by subset like envelope
// cases; arrays match by exact length with per-element subset matching; and an expected
// empty array matches an actual that is empty or absent (writers may omit empty
// collections).
func meshSubsetMismatches(path string, expected, actual any) []string {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualMap, ok := actual.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: expected an object, got %T", path, actual)}
		}
		var mismatches []string
		for key, expectedField := range expectedValue {
			actualField, present := actualMap[key]
			if !present {
				if expectedArray, isArray := expectedField.([]any); isArray && len(expectedArray) == 0 {
					continue // expected [] matches an omitted empty collection
				}
				mismatches = append(mismatches, fmt.Sprintf("%s: missing key %q", path, key))
				continue
			}
			mismatches = append(mismatches, meshSubsetMismatches(path+"."+key, expectedField, actualField)...)
		}
		return mismatches
	case []any:
		actualArray, ok := actual.([]any)
		if !ok && actual == nil && len(expectedValue) == 0 {
			return nil
		}
		if !ok {
			return []string{fmt.Sprintf("%s: expected an array, got %T", path, actual)}
		}
		if len(actualArray) != len(expectedValue) {
			return []string{fmt.Sprintf("%s: expected %d elements, got %d (%v)", path, len(expectedValue), len(actualArray), actualArray)}
		}
		var mismatches []string
		for i := range expectedValue {
			mismatches = append(mismatches, meshSubsetMismatches(fmt.Sprintf("%s[%d]", path, i), expectedValue[i], actualArray[i])...)
		}
		return mismatches
	default:
		if !reflect.DeepEqual(expected, actual) {
			return []string{fmt.Sprintf("%s: expected %v, got %v", path, expected, actual)}
		}
		return nil
	}
}
