package contractdoc

import (
	"errors"
	"strings"
	"testing"
)

const minimalDoc = `{
  "schemaDialect": "openapi-3.0",
  "service": "orders",
  "requests": [
    {"topic": "orders:create", "request": {"$ref": "#/components/schemas/CreateOrder"}, "response": {"$ref": "#/components/schemas/OrderDto"}},
    {"topic": "orders:void", "version": "v2", "request": {"type": "object"}},
    {"topic": "benzene:healthcheck", "request": {}, "response": {}},
    "not an entry"
  ],
  "events": [
    {"topic": "order:created", "message": {"$ref": "#/components/schemas/OrderCreated"}},
    {"topic": "benzene:x", "message": {}}
  ],
  "sends": [
    {"topic": "payment:take", "request": {"type": "object"}}
  ],
  "components": {"schemas": {
    "CreateOrder": {"type": "object", "properties": {"customerId": {"type": "string"}}},
    "OrderDto": {"type": "object", "properties": {"id": {"type": "string"}}},
    "OrderCreated": {"type": "object"}
  }}
}`

func mustParse(t *testing.T, raw string) *Document {
	t.Helper()
	doc, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestParse_Malformed(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestParse_RejectsAnUnknownSchemaDialect(t *testing.T) {
	for _, raw := range []string{`{"schemaDialect": "json-schema-2020-12"}`, `{"openapi": "3.0.1"}`, `{"schemaDialect": 3}`} {
		_, err := Parse([]byte(raw))
		var dialectErr *UnknownSchemaDialectError
		if !errors.As(err, &dialectErr) || !strings.Contains(err.Error(), "openapi-3.0") {
			t.Errorf("%s: err = %v, want *UnknownSchemaDialectError", raw, err)
		}
	}
}

func TestDocumentAccessors(t *testing.T) {
	doc := mustParse(t, minimalDoc)

	if doc.SchemaDialect() != "openapi-3.0" || doc.Service() != "orders" {
		t.Errorf("SchemaDialect() = %q, Service() = %q", doc.SchemaDialect(), doc.Service())
	}
	// benzene: entries, and entries that are not objects, are ignored.
	if len(doc.Requests()) != 2 || len(doc.Events()) != 1 || len(doc.Sends()) != 1 {
		t.Errorf("Requests/Events/Sends = %d/%d/%d, want 2/1/1", len(doc.Requests()), len(doc.Events()), len(doc.Sends()))
	}
	if len(doc.Schemas()) != 3 {
		t.Errorf("Schemas() len = %d, want 3", len(doc.Schemas()))
	}
	if len((&Document{Data: map[string]any{}}).Schemas()) != 0 {
		t.Error("Schemas() of a document without components should be empty")
	}

	create := doc.RequestByTopic("orders:create")
	if create == nil || RequestTopic(create) != "orders:create" || !ResponsePresent(create) {
		t.Fatalf("RequestByTopic(orders:create) = %v", create)
	}
	if _, present := RequestVersion(create); present {
		t.Error("orders:create should have no version")
	}
	void := doc.RequestByTopic("orders:void")
	if v, present := RequestVersion(void); !present || v != "v2" || ResponsePresent(void) {
		t.Errorf("orders:void version = (%q, %v), responsePresent = %v", v, present, ResponsePresent(void))
	}
	if doc.RequestByTopic("nope") != nil || doc.RequestByTopic("benzene:healthcheck") != nil {
		t.Error("RequestByTopic must not find an unknown or a benzene: topic")
	}
}

func TestSortedTopics_DistinctAndSorted(t *testing.T) {
	doc := mustParse(t, `{"schemaDialect": "openapi-3.0", "requests": [
		{"topic": "b", "version": "v1", "request": {}}, {"topic": "b", "version": "v2", "request": {}}, {"topic": "a", "request": {}}]}`)
	assertStringSlice(t, doc.SortedTopics(), []string{"a", "b"})
}

func TestMarshalJSON(t *testing.T) {
	doc := mustParse(t, `{"schemaDialect": "openapi-3.0", "service": "s"}`)
	raw, err := doc.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	roundTripped, err := Parse(raw)
	if err != nil || roundTripped.Service() != "s" {
		t.Errorf("round trip = %v, %v", roundTripped, err)
	}
}
