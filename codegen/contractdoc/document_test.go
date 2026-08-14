package contractdoc

import "testing"

const minimalDoc = `{
  "openapi": "3.0.1",
  "info": {"title": "orders-api", "version": "1.0.0"},
  "requests": [
    {"topic": "orders:create", "request": {"$ref": "#/components/schemas/CreateOrder"}, "response": {"$ref": "#/components/schemas/OrderDto"}},
    {"topic": "benzene:spec", "reserved": true, "request": {"$ref": "#/components/schemas/Void"}, "response": {"$ref": "#/components/schemas/Void"}},
    {"topic": "benzene:healthcheck", "request": {"$ref": "#/components/schemas/Void"}, "response": {"$ref": "#/components/schemas/Void"}}
  ],
  "events": [
    {"topic": "order:created", "message": {"$ref": "#/components/schemas/OrderCreated"}}
  ],
  "components": {"schemas": {
    "CreateOrder": {"type": "object", "properties": {"customerId": {"type": "string"}}},
    "OrderDto": {"type": "object", "properties": {"id": {"type": "string"}}},
    "OrderCreated": {"type": "object"},
    "Void": {"type": "object", "additionalProperties": false}
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

func TestDocumentAccessors(t *testing.T) {
	doc := mustParse(t, minimalDoc)

	if got := doc.OpenAPI(); got != "3.0.1" {
		t.Errorf("OpenAPI() = %q", got)
	}
	if got := doc.Info()["title"]; got != "orders-api" {
		t.Errorf("Info()[title] = %v", got)
	}
	if len(doc.Requests()) != 3 {
		t.Errorf("Requests() len = %d, want 3", len(doc.Requests()))
	}
	if len(doc.Events()) != 1 {
		t.Errorf("Events() len = %d, want 1", len(doc.Events()))
	}
	if len(doc.Schemas()) != 4 {
		t.Errorf("Schemas() len = %d, want 4", len(doc.Schemas()))
	}

	orderCreate := doc.RequestByTopic("orders:create")
	if orderCreate == nil {
		t.Fatal("RequestByTopic(orders:create) = nil")
	}
	if RequestTopic(orderCreate) != "orders:create" {
		t.Errorf("RequestTopic = %q", RequestTopic(orderCreate))
	}
	if _, present := RequestVersion(orderCreate); present {
		t.Error("orders:create should have no version")
	}
	if IsReserved(orderCreate) {
		t.Error("orders:create should not be reserved")
	}

	if doc.RequestByTopic("nope") != nil {
		t.Error("RequestByTopic(nope) should be nil")
	}
}

func TestReservedDetection(t *testing.T) {
	doc := mustParse(t, minimalDoc)

	byFlag := doc.RequestByTopic("benzene:spec")
	if !RequestReservedFlag(byFlag) {
		t.Error("benzene:spec should carry the reserved flag")
	}
	if !IsReserved(byFlag) {
		t.Error("benzene:spec should be reserved")
	}

	byPrefixOnly := doc.RequestByTopic("benzene:healthcheck")
	if RequestReservedFlag(byPrefixOnly) {
		t.Error("benzene:healthcheck should carry no reserved flag in this fixture")
	}
	if !IsReserved(byPrefixOnly) {
		t.Error("benzene:healthcheck should still be reserved via the benzene: prefix rule")
	}

	if !IsReservedTopicID("benzene:mesh") {
		t.Error("benzene:mesh should be a reserved topic id")
	}
	if IsReservedTopicID("orders:create") {
		t.Error("orders:create should not be a reserved topic id")
	}
	if IsReservedTopicID("benzen") {
		t.Error("a prefix-only-partial match must not be treated as reserved")
	}
}

func TestSortedTopics(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	got := doc.SortedTopics()
	want := []string{"benzene:healthcheck", "benzene:spec", "orders:create"}
	if len(got) != len(want) {
		t.Fatalf("SortedTopics() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SortedTopics()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestVersionPresence(t *testing.T) {
	doc := mustParse(t, `{
		"requests": [
			{"topic": "orders:create", "version": "v2", "request": {}, "response": {}},
			{"topic": "orders:cancel", "request": {}, "response": {}}
		]
	}`)
	versioned := doc.RequestByTopic("orders:create")
	v, present := RequestVersion(versioned)
	if !present || v != "v2" {
		t.Errorf("RequestVersion(orders:create) = (%q, %v), want (v2, true)", v, present)
	}

	unversioned := doc.RequestByTopic("orders:cancel")
	v, present = RequestVersion(unversioned)
	if present || v != "" {
		t.Errorf("RequestVersion(orders:cancel) = (%q, %v), want (\"\", false)", v, present)
	}
}

func TestMarshalJSON(t *testing.T) {
	doc := mustParse(t, `{"openapi": "3.0.1"}`)
	raw, err := doc.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	roundTripped, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(marshaled): %v", err)
	}
	if roundTripped.OpenAPI() != "3.0.1" {
		t.Errorf("round-tripped openapi = %q", roundTripped.OpenAPI())
	}
}
