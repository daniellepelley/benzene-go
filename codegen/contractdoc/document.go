// Package contractdoc parses and manipulates the Benzene Contract Document
// (docs/specification/contract-document.md in daniellepelley/Benzene) that a Benzene service
// commits as its "{Service}.spec.json" - top-level "openapi"/"info"/"requests[]"/"events[]"/
// "components" - and implements the document's generation semantics: reserved-topic detection
// and the topic include-list (§5.1-§5.2), the topic-scoped schema-closure projection (§5.3), and
// the contractHash algorithm (§6, in hash.go).
//
// The document is kept as generic JSON (map[string]any / []any), not a strongly-typed OpenAPI
// schema model: every rule this package implements (topic scoping, the $ref/items/
// additionalProperties/properties/allOf/anyOf/oneOf closure walk, contractHash's normalize step)
// operates structurally on the JSON tree and never needs to interpret a schema's "type"/"format"
// - that interpretation is the Go type builder's job (see the sibling gengo package), not this
// one's. Keeping this package generic-JSON-only means it needs no OpenAPI/JSON-Schema parsing
// dependency at all.
package contractdoc

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ReservedTopicPrefix is the prefix contract-document.md §5.1 uses to detect a reserved Benzene
// utility topic when no "reserved" flag is present on the entry.
const ReservedTopicPrefix = "benzene:"

// Document is a parsed Contract Document, held as its generic JSON tree. Every accessor reads
// directly off Data, so a Document returned by ApplyScope/TopicScopedProjection (or built by a
// caller) is just as usable as one from Parse.
type Document struct {
	Data map[string]any
}

// Parse decodes raw as a Contract Document.
func Parse(raw []byte) (*Document, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("contractdoc: parse: %w", err)
	}
	return &Document{Data: data}, nil
}

// Requests returns the document's "requests[]" entries as maps. Entries that are not JSON
// objects (a malformed document) are skipped rather than causing a panic.
func (d *Document) Requests() []map[string]any {
	return objectSlice(d.Data["requests"])
}

// Events returns the document's "events[]" entries as maps.
func (d *Document) Events() []map[string]any {
	return objectSlice(d.Data["events"])
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

// Info returns the document's "info" object.
func (d *Document) Info() map[string]any {
	m, _ := d.Data["info"].(map[string]any)
	return m
}

// OpenAPI returns the top-level "openapi" marker string.
func (d *Document) OpenAPI() string {
	s, _ := d.Data["openapi"].(string)
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
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// RequestTopic returns a requests[] entry's "topic" field.
func RequestTopic(r map[string]any) string {
	s, _ := r["topic"].(string)
	return s
}

// RequestVersion returns a requests[]/events[] entry's "version" field and whether it was
// present at all - contract-document.md §2's "absent and empty are not the same thing" rule.
func RequestVersion(r map[string]any) (version string, present bool) {
	v, ok := r["version"]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

// RequestReservedFlag reports a requests[] entry's own "reserved" flag, ignoring the
// "benzene:" prefix rule - see IsReserved for the full §5.1 detection rule.
func RequestReservedFlag(r map[string]any) bool {
	b, _ := r["reserved"].(bool)
	return b
}

// IsReservedTopicID reports whether topic is a reserved Benzene utility topic by the
// "benzene:" prefix rule of contract-document.md §5.1.
func IsReservedTopicID(topic string) bool {
	return len(topic) >= len(ReservedTopicPrefix) && topic[:len(ReservedTopicPrefix)] == ReservedTopicPrefix
}

// IsReserved implements contract-document.md §5.1's full reserved-detection rule: a requests[]
// entry is reserved when EITHER its "reserved" flag is true OR its topic starts with
// "benzene:". Both are checked - not only the flag - because a document from an older producer
// build may carry a reserved topic with no flag at all.
func IsReserved(r map[string]any) bool {
	return RequestReservedFlag(r) || IsReservedTopicID(RequestTopic(r))
}

// SortedTopics returns every requests[] entry's topic, sorted, for error messages and tests.
func (d *Document) SortedTopics() []string {
	requests := d.Requests()
	topics := make([]string, 0, len(requests))
	for _, r := range requests {
		topics = append(topics, RequestTopic(r))
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
