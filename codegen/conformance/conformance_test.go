// Package conformance runs this module's contractdoc package against the language-neutral
// fixtures vendored from daniellepelley/Benzene's docs/specification/conformance/ into this
// repo's conformance/testdata/ (see conformance/README.md for how they got here and how to
// re-sync them). Passing these fixtures is what "client-generation conformance" means per that
// file's fixture-claims table. Reads the shared testdata directory by relative file path (not a
// Go import), so this module needs no dependency on the root module to run these cases.
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

// --- contract-document-cases.json ---

type documentCasesFixture struct {
	Documents          map[string]json.RawMessage `json:"documents"`
	ParseCases         []parseCase                `json:"parseCases"`
	TopicScopeCases    []topicScopeCase           `json:"topicScopeCases"`
	SchemaClosureCases []schemaClosureCase        `json:"schemaClosureCases"`
}

type scopeOptionsJSON struct {
	Topics          []string `json:"topics"`
	IncludeReserved bool     `json:"includeReserved"`
}

type parseCase struct {
	Name          string            `json:"name"`
	DocumentRef   string            `json:"documentRef"`
	Options       *scopeOptionsJSON `json:"options"`
	Expected      *expectedParse    `json:"expected"`
	ExpectedError *expectedError    `json:"expectedError"`
}

type expectedParse struct {
	OpenAPI  *string         `json:"openapi"`
	Requests []expectedEntry `json:"requests"`
	Events   []expectedEntry `json:"events"`
}

type expectedEntry struct {
	Topic          string  `json:"topic"`
	VersionPresent *bool   `json:"versionPresent"`
	Version        *string `json:"version"`
	Reserved       *bool   `json:"reserved"`
}

type expectedError struct {
	UnknownTopics []string `json:"unknownTopics"`
	ValidTopics   []string `json:"validTopics"`
}

type topicScopeCase struct {
	Name           string           `json:"name"`
	DocumentRef    string           `json:"documentRef"`
	Options        scopeOptionsJSON `json:"options"`
	ExpectedTopics []string         `json:"expectedTopics"`
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

	docFor := func(t *testing.T, ref string) *contractdoc.Document {
		t.Helper()
		raw, ok := fixture.Documents[ref]
		if !ok {
			t.Fatalf("fixture has no document %q", ref)
		}
		doc, err := contractdoc.Parse(raw)
		if err != nil {
			t.Fatalf("parse document %q: %v", ref, err)
		}
		return doc
	}

	t.Run("parseCases", func(t *testing.T) {
		for _, c := range fixture.ParseCases {
			t.Run(c.Name, func(t *testing.T) {
				doc := docFor(t, c.DocumentRef)

				if c.ExpectedError != nil {
					opts := contractdoc.ScopeOptions{}
					if c.Options != nil {
						opts.Topics = c.Options.Topics
						opts.IncludeReserved = c.Options.IncludeReserved
					}
					_, err := contractdoc.ApplyScope(doc, opts)
					var unknownErr *contractdoc.UnknownTopicsError
					if !errors.As(err, &unknownErr) {
						t.Fatalf("ApplyScope error = %v, want *UnknownTopicsError", err)
					}
					assertSet(t, "unknownTopics", unknownErr.Unknown, c.ExpectedError.UnknownTopics)
					assertSet(t, "validTopics", unknownErr.Valid, c.ExpectedError.ValidTopics)
					return
				}

				if c.Expected == nil {
					t.Fatal("case has neither expected nor expectedError")
				}
				if c.Expected.OpenAPI != nil && doc.OpenAPI() != *c.Expected.OpenAPI {
					t.Errorf("openapi = %q, want %q", doc.OpenAPI(), *c.Expected.OpenAPI)
				}
				assertEntries(t, "requests", doc.Requests(), c.Expected.Requests)
				assertEntries(t, "events", doc.Events(), c.Expected.Events)
			})
		}
	})

	t.Run("topicScopeCases", func(t *testing.T) {
		for _, c := range fixture.TopicScopeCases {
			t.Run(c.Name, func(t *testing.T) {
				doc := docFor(t, c.DocumentRef)
				opts := contractdoc.ScopeOptions{Topics: c.Options.Topics, IncludeReserved: c.Options.IncludeReserved}
				scoped, err := contractdoc.ApplyScope(doc, opts)
				if err != nil {
					t.Fatalf("ApplyScope: %v", err)
				}
				var got []string
				for _, r := range scoped.Requests() {
					got = append(got, contractdoc.RequestTopic(r))
				}
				assertSet(t, "expectedTopics", got, c.ExpectedTopics)
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

func assertEntries(t *testing.T, kind string, actual []map[string]any, expected []expectedEntry) {
	t.Helper()
	index := make(map[string]map[string]any, len(actual))
	for _, a := range actual {
		index[contractdoc.RequestTopic(a)] = a
	}
	for _, exp := range expected {
		a, ok := index[exp.Topic]
		if !ok {
			t.Errorf("%s: missing topic %q among %v", kind, exp.Topic, keys(index))
			continue
		}
		if exp.VersionPresent != nil {
			_, present := contractdoc.RequestVersion(a)
			if present != *exp.VersionPresent {
				t.Errorf("%s %q: versionPresent = %v, want %v", kind, exp.Topic, present, *exp.VersionPresent)
			}
		}
		if exp.Version != nil {
			v, _ := contractdoc.RequestVersion(a)
			if v != *exp.Version {
				t.Errorf("%s %q: version = %q, want %q", kind, exp.Topic, v, *exp.Version)
			}
		}
		if exp.Reserved != nil {
			if got := contractdoc.IsReserved(a); got != *exp.Reserved {
				t.Errorf("%s %q: reserved = %v, want %v", kind, exp.Topic, got, *exp.Reserved)
			}
		}
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertSet(t *testing.T, name string, actual, expected []string) {
	t.Helper()
	a := append([]string(nil), actual...)
	e := append([]string(nil), expected...)
	sort.Strings(a)
	sort.Strings(e)
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
	Name         string          `json:"name"`
	Document     json.RawMessage `json:"document"`
	ExpectedHash string          `json:"expectedHash"`
}

func TestConformance_ContractHash(t *testing.T) {
	var fixture hashCasesFixture
	loadFixture(t, "contract-hash-cases.json", &fixture)
	requireCases(t, len(fixture.Cases), "contract-hash-cases", "cases")

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			doc, err := contractdoc.Parse(c.Document)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			// None of the four fixture cases carry a surviving reserved entry under
			// topicScoped=true's distinct behavior (see contractdoc.Hash's doc comment): the
			// only case with reserved entries expects them stripped entirely, which is
			// topicScoped=false's (the whole-service/service-level) behavior - the one every
			// case here is written against.
			got, err := contractdoc.Hash(doc, false)
			if err != nil {
				t.Fatalf("hash: %v", err)
			}
			if got != c.ExpectedHash {
				t.Errorf("hash = %s, want %s", got, c.ExpectedHash)
			}
		})
	}
}
