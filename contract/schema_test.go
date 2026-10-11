package contract

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/daniellepelley/benzene-go/internal/jcs"
)

type textID struct{ v string }

func (t textID) MarshalText() ([]byte, error) { return []byte(t.v), nil }

type custom struct{}

func (custom) MarshalJSON() ([]byte, error) { return []byte(`1`), nil }

type ptrCustom struct{}

func (*ptrCustom) MarshalJSON() ([]byte, error) { return []byte(`1`), nil }

type ptrText struct{}

func (*ptrText) MarshalText() ([]byte, error) { return []byte(`x`), nil }

type level string

type inner struct {
	A string `json:"a"`
}

type Embedded struct {
	Shared string `json:"shared"`
	Deep   string `json:"deep"`
}

type embeddedPtr struct {
	ViaPointer string `json:"viaPointer"`
}

type unexportedEmbedded int

type withEmbedding struct {
	Embedded
	*embeddedPtr
	unexportedEmbedded
	Shared  int    `json:"shared"`
	Renamed inner  `json:"renamed,omitempty"`
	Arr     [2]int `json:"arr,omitempty"`
	Empty   [0]int `json:"empty,omitempty"`
	Opt     string `json:"opt,omitempty"`
	Zero    string `json:"zero,omitzero"`
	Skipped string `json:"-"`
	hidden  string
	Quoted  int    `json:"quoted,string"`
	QPtr    *int   `json:"qptr,string"`
	QSlice  []int  `json:"qslice,string"`
	NoTag   string //nolint:revive // exercises the untagged name
}

type conflictA struct {
	X string
}
type conflictB struct {
	X string
}
type ambiguous struct {
	conflictA
	conflictB
	Y string
}
type taggedWins struct {
	conflictA
	B struct {
		X string `json:"X"`
	} `json:"-"`
	conflictC
}
type conflictC struct {
	X string `json:"X"`
}

type Page[T any] struct {
	Items []T `json:"items"`
	Next  *Page[T]
}

type Declared[T any] struct {
	Item T `json:"item"`
}

func (Declared[T]) BenzeneSchemaName() string { return "Declared Thing!" }

type PtrDeclared struct{}

func (*PtrDeclared) BenzeneSchemaName() string { return "Pointered" }

// selfEmbedding embeds itself through a pointer, an embedding cycle encoding/json tolerates.
type selfEmbedding struct {
	*selfEmbedding
	Name string `json:"name"`
}

type Recursive struct {
	Self *Recursive  `json:"self"`
	Many []Recursive `json:"many"`
}

func derive(t *testing.T, v any) (Schema, map[string]Schema) {
	t.Helper()
	d := newDeriver()
	s := d.schema(reflect.TypeOf(v).Elem(), nil)
	return s, d.schemas
}

func canon(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSchema_Scalars(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{(*string)(nil), `{"type":"string"}`},
		{(*bool)(nil), `{"type":"boolean"}`},
		{(*int)(nil), `{"format":"int64","type":"integer"}`},
		{(*uint32)(nil), `{"format":"int64","type":"integer"}`},
		{(*int16)(nil), `{"format":"int32","type":"integer"}`},
		{(*uint64)(nil), `{"type":"integer"}`},
		{(*float32)(nil), `{"format":"float","type":"number"}`},
		{(*float64)(nil), `{"format":"double","type":"number"}`},
		{(*time.Time)(nil), `{"format":"date-time","type":"string"}`},
		{(*json.RawMessage)(nil), `{}`},
		{(*any)(nil), `{}`},
		{(*textID)(nil), `{"type":"string"}`},
		{(*custom)(nil), `{}`},
		{(*level)(nil), `{"type":"string"}`},
		{(*[]byte)(nil), `{"format":"byte","type":"string"}`},
		{(*[]string)(nil), `{"items":{"type":"string"},"type":"array"}`},
		{(*[2]bool)(nil), `{"items":{"type":"boolean"},"type":"array"}`},
		{(*map[string]int8)(nil), `{"additionalProperties":{"format":"int32","type":"integer"},"type":"object"}`},
		{(*map[int]string)(nil), `{"additionalProperties":{"type":"string"},"type":"object"}`},
		{(*map[textID]string)(nil), `{"additionalProperties":{"type":"string"},"type":"object"}`},
		{(*map[[2]int]string)(nil), `{}`},
		{(*chan int)(nil), `{}`},
		{(*func())(nil), `{}`},
		{(*struct{})(nil), `{"type":"object"}`},
	}
	for _, c := range cases {
		got, _ := derive(t, c.value)
		if g := canon(t, got); g != c.want {
			t.Errorf("%T: got %s, want %s", c.value, g, c.want)
		}
	}
}

func TestSchema_Nullables(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{(**string)(nil), `{"nullable":true,"type":"string"}`},
		{(***string)(nil), `{"nullable":true,"type":"string"}`},
		{(**[]int)(nil), `{"items":{"format":"int64","type":"integer"},"nullable":true,"type":"array"}`},
		{(**any)(nil), `{}`},
		{(**time.Time)(nil), `{"format":"date-time","nullable":true,"type":"string"}`},
		{(**custom)(nil), `{}`},
		{(**ptrCustom)(nil), `{}`},
		{(**ptrText)(nil), `{"nullable":true,"type":"string"}`},
		{(**inner)(nil), `{"allOf":[{"$ref":"#/components/schemas/github.com/daniellepelley/benzene-go/contract.inner"}],"nullable":true,"type":"object"}`},
		{(***inner)(nil), `{"allOf":[{"$ref":"#/components/schemas/github.com/daniellepelley/benzene-go/contract.inner"}],"nullable":true,"type":"object"}`},
		{(**struct{ A int })(nil), `{"nullable":true,"properties":{"A":{"format":"int64","type":"integer"}},"required":["A"],"type":"object"}`},
		{(**Page[int])(nil), `{"allOf":[{"properties":{"Next":{},"items":{"items":{"format":"int64","type":"integer"},"type":"array"}},"required":["Next","items"],"type":"object"}],"nullable":true,"type":"object"}`},
	}
	for _, c := range cases {
		got, _ := derive(t, c.value)
		if g := canon(t, got); g != c.want {
			t.Errorf("%T: got %s, want %s", c.value, g, c.want)
		}
	}
}

func TestSchema_StructFieldsFollowEncodingJSON(t *testing.T) {
	_, components := derive(t, (*withEmbedding)(nil))
	got := canon(t, components["github.com/daniellepelley/benzene-go/contract.withEmbedding"])
	want := `{"properties":{"NoTag":{"type":"string"},"arr":{"items":{"format":"int64","type":"integer"},"type":"array"},` +
		`"deep":{"type":"string"},"empty":{"items":{"format":"int64","type":"integer"},"type":"array"},` +
		`"opt":{"type":"string"},"qptr":{"nullable":true,"type":"string"},"qslice":{"items":{"format":"int64","type":"integer"},"type":"array"},` +
		`"quoted":{"type":"string"},"renamed":{"$ref":"#/components/schemas/github.com/daniellepelley/benzene-go/contract.inner"},` +
		`"shared":{"format":"int64","type":"integer"},"viaPointer":{"type":"string"},"zero":{"type":"string"}},` +
		`"required":["NoTag","arr","deep","qptr","qslice","quoted","renamed","shared"],"type":"object"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSchema_FieldConflicts(t *testing.T) {
	_, components := derive(t, (*ambiguous)(nil))
	if got := canon(t, components["github.com/daniellepelley/benzene-go/contract.ambiguous"]); got != `{"properties":{"Y":{"type":"string"}},"required":["Y"],"type":"object"}` {
		t.Errorf("ambiguous: %s (two untagged X at one depth cancel out)", got)
	}
	_, components = derive(t, (*taggedWins)(nil))
	if got := canon(t, components["github.com/daniellepelley/benzene-go/contract.taggedWins"]); got != `{"properties":{"X":{"type":"string"}},"required":["X"],"type":"object"}` {
		t.Errorf("taggedWins: %s", got)
	}
}

func TestSchema_HoistingAndNames(t *testing.T) {
	d := newDeriver()
	d.schema(reflect.TypeOf(Recursive{}), nil)
	d.schema(reflect.TypeOf(Declared[int]{}), nil)
	d.schema(reflect.TypeOf(PtrDeclared{}), nil)
	d.schema(reflect.TypeOf(Page[string]{}), nil)

	names := map[string]string{}
	for key, name := range d.names {
		names[name] = key
	}
	for _, want := range []string{"Recursive", "DeclaredThing", "Pointered"} {
		if _, ok := names[want]; !ok {
			t.Errorf("no component named %q among %v", want, d.names)
		}
	}
	if len(d.names) != 3 {
		t.Errorf("names = %v: an undeclared generic instance is inlined, not hoisted", d.names)
	}
	key := names["Recursive"]
	if got := canon(t, d.schemas[key]); got != `{"properties":{"many":{"items":{"$ref":"#/components/schemas/`+key+`"},"type":"array"},"self":{"allOf":[{"$ref":"#/components/schemas/`+key+`"}],"nullable":true,"type":"object"}},"required":["many","self"],"type":"object"}` {
		t.Errorf("Recursive = %s", got)
	}
}

func TestSchema_EmbeddingCycleTerminates(t *testing.T) {
	_, components := derive(t, (*selfEmbedding)(nil))
	if got := canon(t, components["github.com/daniellepelley/benzene-go/contract.selfEmbedding"]); got != `{"properties":{"name":{"type":"string"}},"required":["name"],"type":"object"}` {
		t.Errorf("selfEmbedding = %s", got)
	}
}

func TestQualifiedName_UnnamedType(t *testing.T) {
	if got := qualifiedName(reflect.TypeOf(0)); got != "int" {
		t.Errorf("qualifiedName(int) = %q", got)
	}
}
