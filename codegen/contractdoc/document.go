// Package contractdoc parses and manipulates the Benzene Contract Document
// (docs/specification/contract-document.md in daniellepelley/Benzene) that a Benzene service
// serves on benzene:spec and commits as its "{Service}.spec.json" - top-level "schemaDialect"/
// "service"/"requests[]"/"events[]"/"sends[]"/"components" - and implements the document's
// generation semantics: the topic include-list (§5.2), the topic-scoped schema-closure projection
// (§5.3), and the two hashes (§6, in hash.go, computed by the root module's contract package so a
// generated client and a serving service hash one way).
//
// The document is kept as generic JSON (map[string]any / []any), not a strongly-typed OpenAPI
// schema model: every rule this package implements (topic scoping, the $ref/items/
// additionalProperties/properties/allOf/anyOf/oneOf closure walk) operates structurally on the JSON
// tree and never needs to interpret a schema's "type"/"format" - that interpretation is the Go type
// builder's job (see the sibling gengo package), not this one's.
package contractdoc

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/daniellepelley/benzene-go/contract"
)

// UnknownSchemaDialectError is Parse's failure for a document whose schemaDialect this generator
// does not know (contract-document.md §1: a consumer MUST reject it) - including a document with
// none, such as one written before the dialect field existed.
type UnknownSchemaDialectError struct {
	// Dialect is the document's schemaDialect ("" when absent or not a string).
	Dialect string
}

func (e *UnknownSchemaDialectError) Error() string {
	return fmt.Sprintf("contractdoc: unknown schemaDialect %q (this generator reads %q)", e.Dialect, contract.SchemaDialect)
}

// Document is a parsed Contract Document, held as its generic JSON tree. Every accessor reads
// directly off Data, so a Document returned by ApplyScope/TopicScopedProjection (or built by a
// caller) is just as usable as one from Parse.
type Document struct {
	Data map[string]any
}

// Parse decodes raw as a Contract Document, rejecting one whose schemaDialect is not
// contract.SchemaDialect with an *UnknownSchemaDialectError.
func Parse(raw []byte) (*Document, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("contractdoc: parse: %w", err)
	}
	if dialect, _ := data["schemaDialect"].(string); dialect != contract.SchemaDialect {
		return nil, &UnknownSchemaDialectError{Dialect: dialect}
	}
	return &Document{Data: data}, nil
}

// Requests returns the document's "requests[]" entries as maps. A benzene: entry - which a
// producer must not emit - is ignored (contract-document.md §5.1), and entries that are not JSON
// objects (a malformed document) are skipped rather than causing a panic.
func (d *Document) Requests() []map[string]any {
	return objectSlice(d.Data["requests"])
}

// Events returns the document's "events[]" entries as maps, benzene: entries ignored.
func (d *Document) Events() []map[string]any {
	return objectSlice(d.Data["events"])
}

// Sends returns the document's "sends[]" entries as maps, benzene: entries ignored.
func (d *Document) Sends() []map[string]any {
	return objectSlice(d.Data["sends"])
}

// Components returns the document's "components" object (nil if absent).
func (d *Document) Components() map[string]any {
	m, _ := d.Data["components"].(map[string]any)
	return m
}

// Schemas returns "components.schemas" as a name -> schema-object map (empty if absent).
func (d *Document) Schemas() map[string]any {
	schemas, _ := d.Components()["schemas"].(map[string]any)
	if schemas == nil {
		return map[string]any{}
	}
	return schemas
}

// SchemaDialect returns the document's "schemaDialect".
func (d *Document) SchemaDialect() string {
	s, _ := d.Data["schemaDialect"].(string)
	return s
}

// Service returns the document's "service" name.
func (d *Document) Service() string {
	s, _ := d.Data["service"].(string)
	return s
}

// MarshalJSON serializes the document's raw JSON tree.
func (d *Document) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Data)
}

func objectSlice(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok && !contract.IsReserved(RequestTopic(m)) {
			out = append(out, m)
		}
	}
	return out
}

// RequestTopic returns a requests[]/events[]/sends[] entry's "topic" field.
func RequestTopic(r map[string]any) string {
	s, _ := r["topic"].(string)
	return s
}

// RequestVersion returns an entry's "version" field and whether it was present at all - an
// unversioned entry omits it (contract-document.md §2).
func RequestVersion(r map[string]any) (version string, present bool) {
	v, ok := r["version"]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

// ResponsePresent reports whether a requests[]/sends[] entry carries a "response" member at all:
// absent means no payload (a request) or one-way (a send), while {} means a payload of any shape
// (contract-document.md §2, §3.2).
func ResponsePresent(r map[string]any) bool {
	_, ok := r["response"]
	return ok
}

// SortedTopics returns the distinct topics of the requests[] entries, sorted, for error messages
// and tests (a topic served at two versions is listed once).
func (d *Document) SortedTopics() []string {
	seen := map[string]bool{}
	topics := []string{}
	for _, r := range d.Requests() {
		if topic := RequestTopic(r); !seen[topic] {
			seen[topic] = true
			topics = append(topics, topic)
		}
	}
	sort.Strings(topics)
	return topics
}

// RequestByTopic returns the requests[] entry for topic, or nil if not present.
func (d *Document) RequestByTopic(topic string) map[string]any {
	for _, r := range d.Requests() {
		if RequestTopic(r) == topic {
			return r
		}
	}
	return nil
}

// clone returns a shallow copy of the document's top-level map, so a projection can replace a
// field without mutating the source Document.
func (d *Document) clone() map[string]any {
	out := make(map[string]any, len(d.Data))
	for k, v := range d.Data {
		out[k] = v
	}
	return out
}

func toAnySlice(entries []map[string]any) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e
	}
	return out
}
