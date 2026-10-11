// Package conformance runs this module's contractdoc package against the language-neutral
// fixtures vendored from daniellepelley/Benzene's docs/specification/conformance/ into this
// repo's conformance/testdata/ (see conformance/README.md for how they got here and how to
// re-sync them). Passing these fixtures is what "client-generation conformance" means per that
// file's fixture-claims table. Reads the shared testdata directory by relative file path. The
// producer groups (derivation, naming, benzene:spec) run in the root module's runner.
package conformance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/daniellepelley/benzene-go/codegen/contractdoc"
)

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	path := filepath.Join("..", "..", "conformance", "testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
}

// requireCases fails the test when a fixture list the runner is about to iterate is empty.
//
// json.Unmarshal leaves a slice nil when the fixture has no such key, and `for range` over nil
// iterates nothing - so a renamed key upstream does not break the test, it disables it, and a run
// that checked nothing is indistinguishable from a clean pass. These fixtures are vendored
// snapshots of a canonical set that other people rename; the runner is the only thing positioned to
// notice the drift.
func requireCases(t *testing.T, n int, fixture, key string) {
	t.Helper()
	if n == 0 {
		t.Fatalf("%s: fixture list %q is empty - the runner and the fixture have drifted", fixture, key)
	}
}

// --- contract-document-cases.json (client-generator groups) ---

type documentCasesFixture struct {
	Documents          map[string]json.RawMessage `json:"documents"`
	ParseCases         []parseCase                `json:"parseCases"`
	TopicScopeCases    []topicScopeCase           `json:"topicScopeCases"`
	SchemaClosureCases []schemaClosureCase        `json:"schemaClosureCases"`
}

type scopeOptionsJSON struct {
	Topics []string `json:"topics"`
}

type parseCase struct {
	Name          string            `json:"name"`
	DocumentRef   string            `json:"documentRef"`
	Options       *scopeOptionsJSON `json:"options"`
	Expected      *expectedParse    `json:"expected"`
	ExpectedError *expectedError    `json:"expectedError"`
}

type expectedParse struct {
	SchemaDialect *string         `json:"schemaDialect"`
	Service       *string         `json:"service"`
	Requests      []expectedEntry `json:"requests"`
	Events        []expectedEntry `json:"events"`
	Sends         []expectedEntry `json:"sends"`
}

type expectedEntry struct {
	Topic           string  `json:"topic"`
	VersionPresent  *bool   `json:"versionPresent"`
	Version         *string `json:"version"`
	ResponsePresent *bool   `json:"responsePresent"`
}

type expectedError struct {
	Kind          string   `json:"kind"`
	UnknownTopics []string `json:"unknownTopics"`
	ValidTopics   []string `json:"validTopics"`
}

type topicScopeCase struct {
	Name           string           `json:"name"`
	DocumentRef    string           `json:"documentRef"`
	Options        scopeOptionsJSON `json:"options"`
	ExpectedTopics []string         `json:"expectedTopics"`
	ExpectedEvents []string         `json:"expectedEvents"`
	ExpectedSends  []string         `json:"expectedSends"`
}

type schemaClosureCase struct {
	Name               string   `json:"name"`
	DocumentRef        string   `json:"documentRef"`
	Topic              string   `json:"topic"`
	ExpectedComponents []string `json:"expectedComponents"`
}

func TestConformance_ContractDocument(t *testing.T) {
	var fixture documentCasesFixture
	loadFixture(t, "contract-document-cases.json", &fixture)
	requireCases(t, len(fixture.ParseCases), "contract-document-cases", "parseCases")
	requireCases(t, len(fixture.TopicScopeCases), "contract-document-cases", "topicScopeCases")
	requireCases(t, len(fixture.SchemaClosureCases), "contract-document-cases", "schemaClosureCases")

	rawFor := func(t *testing.T, ref string) json.RawMessage {
		t.Helper()
		raw, ok := fixture.Documents[ref]
		if !ok {
			t.Fatalf("fixture has no document %q", ref)
		}
		return raw
	}
	docFor := func(t *testing.T, ref string) *contractdoc.Document {
		t.Helper()
		doc, err := contractdoc.Parse(rawFor(t, ref))
		if err != nil {
			t.Fatalf("parse document %q: %v", ref, err)
		}
		return doc
	}

	t.Run("parseCases", func(t *testing.T) {
		for _, c := range fixture.ParseCases {
			t.Run(c.Name, func(t *testing.T) {
				if c.ExpectedError != nil {
					checkParseError(t, rawFor(t, c.DocumentRef), c)
					return
				}
				if c.Expected == nil {
					t.Fatal("case has neither expected nor expectedError")
				}
				doc := docFor(t, c.DocumentRef)
				if c.Expected.SchemaDialect != nil && doc.SchemaDialect() != *c.Expected.SchemaDialect {
					t.Errorf("schemaDialect = %q, want %q", doc.SchemaDialect(), *c.Expected.SchemaDialect)
				}
				if c.Expected.Service != nil && doc.Service() != *c.Expected.Service {
					t.Errorf("service = %q, want %q", doc.Service(), *c.Expected.Service)
				}
				assertEntries(t, "requests", doc.Requests(), c.Expected.Requests)
				assertEntries(t, "events", doc.Events(), c.Expected.Events)
				assertEntries(t, "sends", doc.Sends(), c.Expected.Sends)
			})
		}
	})

	t.Run("topicScopeCases", func(t *testing.T) {
		for _, c := range fixture.TopicScopeCases {
			t.Run(c.Name, func(t *testing.T) {
				doc := docFor(t, c.DocumentRef)
				scoped, err := contractdoc.ApplyScope(doc, contractdoc.ScopeOptions{Topics: c.Options.Topics})
				if err != nil {
					t.Fatalf("ApplyScope: %v", err)
				}
				assertSet(t, "expectedTopics", topicsOf(scoped.Requests()), c.ExpectedTopics)
				if c.ExpectedEvents != nil {
					assertSet(t, "expectedEvents", topicsOf(scoped.Events()), c.ExpectedEvents)
				}
				if c.ExpectedSends != nil {
					assertSet(t, "expectedSends", topicsOf(scoped.Sends()), c.ExpectedSends)
				}
			})
		}
	})

	t.Run("schemaClosureCases", func(t *testing.T) {
		for _, c := range fixture.SchemaClosureCases {
			t.Run(c.Name, func(t *testing.T) {
				doc := docFor(t, c.DocumentRef)
				scoped, err := contractdoc.TopicScopedProjection(doc, c.Topic)
				if err != nil {
					t.Fatalf("TopicScopedProjection: %v", err)
				}
				var got []string
				for name := range scoped.Schemas() {
					got = append(got, name)
				}
				assertSet(t, "expectedComponents", got, c.ExpectedComponents)
			})
		}
	})
}

// checkParseError asserts a parse case's expected failure: an unknown schemaDialect fails Parse; an
// include-list naming a topic the document lacks fails ApplyScope, listing both sets.
func checkParseError(t *testing.T, raw json.RawMessage, c parseCase) {
	t.Helper()
	switch c.ExpectedError.Kind {
	case "unknown-schema-dialect":
		_, err := contractdoc.Parse(raw)
		var dialectErr *contractdoc.UnknownSchemaDialectError
		if !errors.As(err, &dialectErr) {
			t.Fatalf("Parse error = %v, want *UnknownSchemaDialectError", err)
		}
	case "unknown-topics":
		doc, err := contractdoc.Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		opts := contractdoc.ScopeOptions{}
		if c.Options != nil {
			opts.Topics = c.Options.Topics
		}
		_, err = contractdoc.ApplyScope(doc, opts)
		var unknownErr *contractdoc.UnknownTopicsError
		if !errors.As(err, &unknownErr) {
			t.Fatalf("ApplyScope error = %v, want *UnknownTopicsError", err)
		}
		assertSet(t, "unknownTopics", unknownErr.Unknown, c.ExpectedError.UnknownTopics)
		assertSet(t, "validTopics", unknownErr.Valid, c.ExpectedError.ValidTopics)
	default:
		t.Fatalf("unknown expectedError kind %q", c.ExpectedError.Kind)
	}
}

func topicsOf(entries []map[string]any) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, contractdoc.RequestTopic(e))
	}
	return out
}

// assertEntries matches expected rows to actual entries by (topic, version) - a row without a
// version keys the unversioned entry, or the topic's only entry - and checks the asserted
// properties; "present: false" means the member is genuinely absent. The row count must match: a
// benzene: entry is ignored, not listed.
func assertEntries(t *testing.T, kind string, actual []map[string]any, expected []expectedEntry) {
	t.Helper()
	if expected == nil {
		return
	}
	if len(actual) != len(expected) {
		t.Errorf("%s: %d entries %v, want %d", kind, len(actual), topicsOf(actual), len(expected))
	}
	for _, exp := range expected {
		a := findEntry(actual, exp)
		if a == nil {
			t.Errorf("%s: no entry for topic %q (version %v) among %v", kind, exp.Topic, exp.Version, topicsOf(actual))
			continue
		}
		if exp.VersionPresent != nil {
			if _, present := contractdoc.RequestVersion(a); present != *exp.VersionPresent {
				t.Errorf("%s %q: versionPresent = %v, want %v", kind, exp.Topic, present, *exp.VersionPresent)
			}
		}
		if exp.ResponsePresent != nil {
			if present := contractdoc.ResponsePresent(a); present != *exp.ResponsePresent {
				t.Errorf("%s %q: responsePresent = %v, want %v", kind, exp.Topic, present, *exp.ResponsePresent)
			}
		}
	}
}

func findEntry(actual []map[string]any, exp expectedEntry) map[string]any {
	var candidates []map[string]any
	for _, a := range actual {
		if contractdoc.RequestTopic(a) != exp.Topic {
			continue
		}
		version, present := contractdoc.RequestVersion(a)
		if exp.Version != nil {
			if present && version == *exp.Version {
				return a
			}
			continue
		}
		if exp.VersionPresent != nil && *exp.VersionPresent != present {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return nil
}

// assertSet compares two lists as sets: order and repetition (a topic served at two versions) do
// not count.
func assertSet(t *testing.T, name string, actual, expected []string) {
	t.Helper()
	a, e := distinctSorted(actual), distinctSorted(expected)
	if len(a) != len(e) {
		t.Errorf("%s: got %v, want %v", name, actual, expected)
		return
	}
	for i := range a {
		if a[i] != e[i] {
			t.Errorf("%s: got %v, want %v", name, actual, expected)
			return
		}
	}
}

// --- contract-hash-cases.json ---

type hashCasesFixture struct {
	Cases []hashCase `json:"cases"`
}

type hashCase struct {
	Name                     string          `json:"name"`
	Document                 json.RawMessage `json:"document"`
	Projection               *projection     `json:"projection"`
	ExpectedContractHash     string          `json:"expectedContractHash"`
	ExpectedRegistrationHash string          `json:"expectedRegistrationHash"`
}

type projection struct {
	Kind   string   `json:"kind"`
	Topic  string   `json:"topic"`
	Topics []string `json:"topics"`
}

// A client generator runs every case: a plain case hashes the document as it stands; a projection
// case projects it first (§5.3 topic-scoped, §5.2 include-list) and hashes the projection.
func TestConformance_ContractHash(t *testing.T) {
	var fixture hashCasesFixture
	loadFixture(t, "contract-hash-cases.json", &fixture)
	requireCases(t, len(fixture.Cases), "contract-hash-cases", "cases")

	projections := 0
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			doc, err := contractdoc.Parse(c.Document)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if c.Projection != nil {
				projections++
				switch c.Projection.Kind {
				case "topic":
					doc, err = contractdoc.TopicScopedProjection(doc, c.Projection.Topic)
				case "include-list":
					doc, err = contractdoc.ApplyScope(doc, contractdoc.ScopeOptions{Topics: c.Projection.Topics})
				default:
					t.Fatalf("unknown projection kind %q", c.Projection.Kind)
				}
				if err != nil {
					t.Fatalf("projection: %v", err)
				}
				// A projection carries its own hashes, never the provider's.
				if doc.Data["contractHash"] != c.ExpectedContractHash || doc.Data["registrationHash"] != c.ExpectedRegistrationHash {
					t.Errorf("projection carries (%v, %v), want its recomputed hashes", doc.Data["contractHash"], doc.Data["registrationHash"])
				}
			}
			contractHash, registrationHash, err := contractdoc.Hashes(doc)
			if err != nil {
				t.Fatalf("hash: %v", err)
			}
			if contractHash != c.ExpectedContractHash {
				t.Errorf("contractHash = %s, want %s", contractHash, c.ExpectedContractHash)
			}
			if registrationHash != c.ExpectedRegistrationHash {
				t.Errorf("registrationHash = %s, want %s", registrationHash, c.ExpectedRegistrationHash)
			}
		})
	}
	requireCases(t, projections, "contract-hash-cases", "cases with a projection")
}

func distinctSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
