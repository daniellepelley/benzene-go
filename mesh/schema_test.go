package mesh

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

type schemaFlat struct {
	Name     string  `json:"name"`
	Count    int     `json:"count,omitempty"`
	Ratio    float64 `json:"ratio"`
	Enabled  bool    `json:"enabled"`
	ignored  string  //lint:ignore U1000 exists to prove unexported fields are skipped
	Skipped  string  `json:"-"`
	Untagged string
	AsString int `json:"asString,string"`
}

type schemaChild struct {
	ID string `json:"id"`
}

type schemaNested struct {
	Child  schemaChild     `json:"child"`
	Opt    *schemaChild    `json:"opt,omitempty"`
	Tags   []string        `json:"tags"`
	Blob   []byte          `json:"blob"`
	Attrs  map[string]int  `json:"attrs"`
	Any    any             `json:"any"`
	When   time.Time       `json:"when"`
	Raw    json.RawMessage `json:"raw"`
	Matrix [2]int          `json:"matrix"`
}

type schemaEmbeds struct {
	schemaChild
	Named schemaChild `json:"named"`
	ID    string      `json:"id"` // conflicts with the promoted schemaChild.ID; first seen wins
}

type schemaPtrEmbed struct {
	*schemaChild
	Extra string `json:"extra"`
}

type schemaTree struct {
	Value    string        `json:"value"`
	Children []*schemaTree `json:"children,omitempty"`
}

type customText struct{}

func (customText) MarshalText() ([]byte, error) { return []byte("x"), nil }

type customJSON struct{}

func (customJSON) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

func TestDeriveSchema_Scalars(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want map[string]any
	}{
		{name: "string", typ: typeOf[string](), want: map[string]any{"type": "string"}},
		{name: "bool", typ: typeOf[bool](), want: map[string]any{"type": "boolean"}},
		{name: "int", typ: typeOf[int](), want: map[string]any{"type": "integer"}},
		{name: "int8", typ: typeOf[int8](), want: map[string]any{"type": "integer"}},
		{name: "uint32", typ: typeOf[uint32](), want: map[string]any{"type": "integer"}},
		{name: "float32", typ: typeOf[float32](), want: map[string]any{"type": "number"}},
		{name: "float64", typ: typeOf[float64](), want: map[string]any{"type": "number"}},
		{name: "time.Time", typ: typeOf[time.Time](), want: map[string]any{"type": "string", "format": "date-time"}},
		{name: "json.RawMessage is unconstrained", typ: typeOf[json.RawMessage](), want: map[string]any{}},
		{name: "any is unconstrained", typ: typeOf[any](), want: map[string]any{}},
		{name: "TextMarshaler is a string", typ: typeOf[customText](), want: map[string]any{"type": "string"}},
		{name: "json.Marshaler is unconstrained", typ: typeOf[customJSON](), want: map[string]any{}},
		{name: "byte slice is a base64 string", typ: typeOf[[]byte](), want: map[string]any{"type": "string"}},
		{name: "unmarshalable kind is unconstrained", typ: typeOf[chan int](), want: map[string]any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveSchema(tt.typ); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("deriveSchema(%v) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestDeriveSchema_NilTypeYieldsNilSchema(t *testing.T) {
	if got := deriveSchema(nil); got != nil {
		t.Errorf("deriveSchema(nil) = %v, want nil", got)
	}
}

func TestDeriveSchema_Pointers(t *testing.T) {
	t.Run("pointer adds null to the pointee type", func(t *testing.T) {
		want := map[string]any{"type": []string{"string", "null"}}
		if got := deriveSchema(typeOf[*string]()); !reflect.DeepEqual(got, want) {
			t.Errorf("deriveSchema(*string) = %v, want %v", got, want)
		}
	})

	t.Run("pointer to pointer stays nullable once", func(t *testing.T) {
		want := map[string]any{"type": []string{"string", "null"}}
		if got := deriveSchema(typeOf[**string]()); !reflect.DeepEqual(got, want) {
			t.Errorf("deriveSchema(**string) = %v, want %v", got, want)
		}
	})
}

func TestDeriveSchema_StructFields(t *testing.T) {
	schema := deriveSchema(typeOf[schemaFlat]())

	if schema["type"] != "object" {
		t.Fatalf(`schema["type"] = %v, want "object"`, schema["type"])
	}
	properties := schema["properties"].(map[string]any)

	wantProperties := map[string]map[string]any{
		"name":     {"type": "string"},
		"count":    {"type": "integer"},
		"ratio":    {"type": "number"},
		"enabled":  {"type": "boolean"},
		"Untagged": {"type": "string"},
		"asString": {"type": "string"}, // the `,string` option marshals the int as a string
	}
	if len(properties) != len(wantProperties) {
		t.Errorf("properties = %v, want exactly %v (unexported and json:\"-\" fields skipped)", properties, wantProperties)
	}
	for name, want := range wantProperties {
		if got, ok := properties[name].(map[string]any); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("properties[%q] = %v, want %v", name, properties[name], want)
		}
	}

	wantRequired := []string{"name", "ratio", "enabled", "Untagged", "asString"} // declaration order, omitempty excluded
	if got := schema["required"].([]string); !reflect.DeepEqual(got, wantRequired) {
		t.Errorf("required = %v, want %v", got, wantRequired)
	}
}

func TestDeriveSchema_NestedAndContainerFields(t *testing.T) {
	properties := deriveSchema(typeOf[schemaNested]())["properties"].(map[string]any)

	tests := []struct {
		field string
		want  map[string]any
	}{
		{field: "child", want: map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		}},
		{field: "opt", want: map[string]any{
			"type":       []string{"object", "null"},
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		}},
		{field: "tags", want: map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		{field: "blob", want: map[string]any{"type": "string"}},
		{field: "attrs", want: map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}}},
		{field: "any", want: map[string]any{}},
		{field: "when", want: map[string]any{"type": "string", "format": "date-time"}},
		{field: "raw", want: map[string]any{}},
		{field: "matrix", want: map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			if got := properties[tt.field]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("properties[%q] = %v, want %v", tt.field, got, tt.want)
			}
		})
	}
}

func TestDeriveSchema_EmbeddedStructsArePromoted(t *testing.T) {
	schema := deriveSchema(typeOf[schemaEmbeds]())
	properties := schema["properties"].(map[string]any)

	if _, ok := properties["id"]; !ok {
		t.Errorf("promoted field %q missing: %v", "id", properties)
	}
	if _, ok := properties["named"]; !ok {
		t.Errorf("named embedded field %q missing: %v", "named", properties)
	}
	if _, ok := properties["schemaChild"]; ok {
		t.Errorf("embedded struct leaked as its own property: %v", properties)
	}
	// The conflicting outer ID must not duplicate the required entry.
	required := schema["required"].([]string)
	seen := map[string]int{}
	for _, name := range required {
		seen[name]++
	}
	if seen["id"] != 1 {
		t.Errorf(`required lists "id" %d times, want once: %v`, seen["id"], required)
	}
}

func TestDeriveSchema_EmbeddedPointerStructsArePromoted(t *testing.T) {
	properties := deriveSchema(typeOf[schemaPtrEmbed]())["properties"].(map[string]any)

	for _, field := range []string{"id", "extra"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("properties missing %q: %v", field, properties)
		}
	}
	if _, ok := properties["schemaChild"]; ok {
		t.Errorf("embedded pointer struct leaked as its own property: %v", properties)
	}
}

func TestDeriveSchema_RecursiveTypeIsCutAtTheCycle(t *testing.T) {
	schema := deriveSchema(typeOf[schemaTree]()) // must terminate

	properties := schema["properties"].(map[string]any)
	children := properties["children"].(map[string]any)
	if children["type"] != "array" {
		t.Fatalf(`children["type"] = %v, want "array"`, children["type"])
	}
	items := children["items"].(map[string]any)
	// items is *schemaTree: the cycle is cut with an unconstrained schema, made nullable
	// by the pointer - so no "type"/"properties" constraints survive at the cycle point.
	if _, constrained := items["properties"]; constrained {
		t.Errorf("cycle was not cut, items = %v", items)
	}
}

func TestDescriptorHash(t *testing.T) {
	info := ServiceInfo{Service: "orders", ServiceVersion: "1.0.0", InstanceID: "orders-1", Placement: Placement{Cloud: "aws"}}
	registry := newTestRegistry(t, benzene.NewTopic("order:create"))

	t.Run("has the sha256 wire format", func(t *testing.T) {
		hash := Describe(registry, info).DescriptorHash
		if len(hash) != len("sha256:")+64 || hash[:7] != "sha256:" {
			t.Errorf("DescriptorHash = %q, want sha256:<64 hex chars>", hash)
		}
	})

	t.Run("is deterministic for the same contract", func(t *testing.T) {
		a := Describe(registry, info).DescriptorHash
		b := Describe(newTestRegistry(t, benzene.NewTopic("order:create")), info).DescriptorHash
		if a != b {
			t.Errorf("hashes differ for identical contracts: %q vs %q", a, b)
		}
	})

	t.Run("ignores the instance id - two copies of one build hash identically", func(t *testing.T) {
		other := info
		other.InstanceID = "orders-2"
		if a, b := Describe(registry, info).DescriptorHash, Describe(registry, other).DescriptorHash; a != b {
			t.Errorf("hashes differ across instances of the same build: %q vs %q", a, b)
		}
	})

	t.Run("ignores transient feed degradation", func(t *testing.T) {
		desc := Describe(nil, info)
		bare := desc
		bare.Degraded = nil
		if a, b := descriptorHash(desc), descriptorHash(bare); a != b {
			t.Errorf("hashes differ on Degraded alone: %q vs %q", a, b)
		}
	})

	t.Run("changes when the topic contract changes", func(t *testing.T) {
		grown := newTestRegistry(t, benzene.NewTopic("order:create"), benzene.NewTopic("order:cancel"))
		if a, b := Describe(registry, info).DescriptorHash, Describe(grown, info).DescriptorHash; a == b {
			t.Errorf("hash did not change when a topic was added: %q", a)
		}
	})

	t.Run("changes when the service version changes", func(t *testing.T) {
		bumped := info
		bumped.ServiceVersion = "1.0.1"
		if a, b := Describe(registry, info).DescriptorHash, Describe(registry, bumped).DescriptorHash; a == b {
			t.Errorf("hash did not change when serviceVersion changed: %q", a)
		}
	})
}

func TestDescriptor_TopicSchemasHaveCamelCaseWireNames(t *testing.T) {
	registry := newTestRegistry(t, benzene.NewTopic("order:create"))
	desc := Describe(registry, ServiceInfo{Service: "orders", Placement: Placement{Cloud: "aws"}})

	data, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw struct {
		Topics []map[string]any `json:"topics"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(raw.Topics) != 1 {
		t.Fatalf("topics = %v, want 1 entry", raw.Topics)
	}
	for _, key := range []string{"requestSchema", "responseSchema"} {
		if _, ok := raw.Topics[0][key]; !ok {
			t.Errorf("marshaled topic is missing key %q: %s", key, data)
		}
	}
}
