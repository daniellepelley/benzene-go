package contract

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

var (
	timeType          = reflect.TypeOf(time.Time{})
	rawMessageType    = reflect.TypeOf(json.RawMessage{})
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*interface {
		MarshalText() ([]byte, error)
	})(nil)).Elem()
	schemaNamerType = reflect.TypeOf((*SchemaNamer)(nil)).Elem()
)

// SchemaNamer is the declared-name hook (contract-document.md §4.1 rule 5): a payload type whose
// value implements it is published under BenzeneSchemaName() instead of its Go type name. It is
// the remedy for a component-name collision - two types named Item in two packages - and the way
// to name a generic instance, which is otherwise inlined. The method must return a constant; it is
// called once, at start-up, on the type's zero value.
//
//	type Item struct {
//		SKU string `json:"sku"`
//	}
//
//	func (Item) BenzeneSchemaName() string { return "OrderItem" }
type SchemaNamer interface {
	BenzeneSchemaName() string
}

// deriver turns Go types into OpenAPI 3.0 schemas (contract-document.md §4.2), hoisting every
// named struct type into components under a provisional key - its package-qualified name, which
// cannot collide - so the public names can be chosen, and their collisions judged, once every
// type is known (derive.go).
type deriver struct {
	schemas map[string]Schema       // provisional key -> schema; nil while being built
	types   map[string]reflect.Type // provisional key -> the type it was derived from
	names   map[string]string       // provisional key -> its component name before collision checks
}

func newDeriver() *deriver {
	return &deriver{schemas: map[string]Schema{}, types: map[string]reflect.Type{}, names: map[string]string{}}
}

// componentName is the name a named struct type is published under: its declared name, else its
// simple Go name - except an undeclared generic instance, whose reflect name (Page[pkg.Order]) is
// not a name, and which is inlined instead (""). Characters a component key may not hold are
// dropped (§4.1 rule 3).
func componentName(t reflect.Type) string {
	var name string
	switch {
	case t.Implements(schemaNamerType):
		name = reflect.Zero(t).Interface().(SchemaNamer).BenzeneSchemaName()
	case reflect.PointerTo(t).Implements(schemaNamerType):
		name = reflect.New(t).Interface().(SchemaNamer).BenzeneSchemaName()
	case !strings.Contains(t.Name(), "["):
		name = t.Name()
	}
	return sanitizeName(name)
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x80 && (r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hoistable reports whether t is a named struct type: what becomes a component.
func hoistable(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t.Name() != "" && t != timeType
}

// qualifiedName identifies a type for provisional keys and error messages.
func qualifiedName(t reflect.Type) string {
	if t.PkgPath() == "" {
		return t.String()
	}
	return t.PkgPath() + "." + t.Name()
}

// schema derives t's schema. visiting holds the inlined named types on the current path, so a
// recursive type that is not hoisted is cut with {} rather than expanded forever.
func (d *deriver) schema(t reflect.Type, visiting []reflect.Type) Schema {
	switch t {
	case timeType:
		return Schema{"type": "string", "format": "date-time"}
	case rawMessageType:
		return Schema{}
	}
	if t.Kind() == reflect.Pointer {
		return d.pointer(t, visiting)
	}
	// A custom json.Marshaler makes the marshaled shape unknowable; a TextMarshaler always writes a
	// string. Value-receiver methods only: those are what encoding/json calls on the non-addressable
	// values a handler returns.
	if t.Implements(jsonMarshalerType) {
		return Schema{}
	}
	if t.Implements(textMarshalerType) {
		return Schema{"type": "string"}
	}

	switch t.Kind() {
	case reflect.String:
		return Schema{"type": "string"}
	case reflect.Bool:
		return Schema{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint32:
		return Schema{"type": "integer", "format": "int64"}
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint8, reflect.Uint16:
		return Schema{"type": "integer", "format": "int32"}
	case reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return Schema{"type": "integer"} // beyond int64: no OpenAPI format describes it
	case reflect.Float32:
		return Schema{"type": "number", "format": "float"}
	case reflect.Float64:
		return Schema{"type": "number", "format": "double"}
	case reflect.Interface:
		return Schema{}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 && !t.Elem().Implements(jsonMarshalerType) && !t.Elem().Implements(textMarshalerType) {
			return Schema{"type": "string", "format": "byte"} // encoding/json base64-encodes []byte
		}
		return Schema{"type": "array", "items": d.schema(t.Elem(), visiting)}
	case reflect.Array:
		return Schema{"type": "array", "items": d.schema(t.Elem(), visiting)}
	case reflect.Map:
		if !stringKeyed(t.Key()) {
			return Schema{} // encoding/json cannot marshal it
		}
		return Schema{"type": "object", "additionalProperties": d.schema(t.Elem(), visiting)}
	case reflect.Struct:
		return d.structSchema(t, visiting)
	}
	return Schema{} // chan, func, complex, unsafe.Pointer: not marshalable
}

// stringKeyed reports whether encoding/json writes a map with key type k as a JSON object.
func stringKeyed(k reflect.Type) bool {
	switch k.Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return k.Implements(textMarshalerType)
}

// pointer derives a pointer's schema: what its target marshals to, or null (§4.2's nullable rows).
func (d *deriver) pointer(t reflect.Type, visiting []reflect.Type) Schema {
	elem := t.Elem()
	if elem != timeType && !elem.Implements(jsonMarshalerType) && t.Implements(jsonMarshalerType) {
		return Schema{} // a pointer-receiver MarshalJSON
	}
	if !elem.Implements(textMarshalerType) && t.Implements(textMarshalerType) {
		return Schema{"type": "string", "nullable": true}
	}
	inner := d.schema(elem, visiting)
	if len(inner) == 0 {
		return inner // {} already admits null; a cut cycle stays cut
	}
	if _, isRef := inner["$ref"]; isRef || (hoistable(elem) && !isNullable(inner)) {
		// A nullable named object, hoisted or inlined: the wrapper, so both dereference the same.
		return Schema{"type": "object", "nullable": true, "allOf": []any{inner}}
	}
	if !isNullable(inner) {
		inner["nullable"] = true
	}
	return inner
}

func isNullable(s Schema) bool { return s["nullable"] == true }

// structSchema derives a struct: a $ref to its component when it is a named type with a name,
// otherwise the object inline.
func (d *deriver) structSchema(t reflect.Type, visiting []reflect.Type) Schema {
	if hoistable(t) {
		if name := componentName(t); name != "" {
			key := qualifiedName(t)
			if _, seen := d.schemas[key]; !seen {
				d.schemas[key] = nil // in progress: a recursive reference finds it and stops here
				d.types[key] = t
				d.names[key] = name
				d.schemas[key] = d.object(t, nil)
			}
			return Schema{"$ref": RefPrefix + key}
		}
		for _, v := range visiting {
			if v == t {
				return Schema{} // an inlined type's cycle: cut
			}
		}
		visiting = append(visiting[:len(visiting):len(visiting)], t)
	}
	return d.object(t, visiting)
}

// object derives a struct's object schema: its JSON properties as encoding/json resolves them, and
// required listing every property the marshaler always writes.
func (d *deriver) object(t reflect.Type, visiting []reflect.Type) Schema {
	properties := Schema{}
	var required []string
	for _, f := range jsonFields(t) {
		if f.quoted {
			s := Schema{"type": "string"}
			if f.typ.Kind() == reflect.Pointer {
				s["nullable"] = true
			}
			properties[f.name] = s
		} else {
			properties[f.name] = d.schema(f.typ, visiting)
		}
		if !f.omittable {
			required = append(required, f.name)
		}
	}
	schema := Schema{"type": "object"}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// jsonField is one property of a struct's marshaled form.
type jsonField struct {
	name      string
	typ       reflect.Type
	depth     int
	tagged    bool
	quoted    bool // the ",string" option on a scalar
	omittable bool // the marshaler may leave it out
}

// jsonFields lists t's JSON properties the way encoding/json resolves them: exported fields under
// their tag names, "-" skipped, untagged embedded structs flattened, and a name claimed at several
// depths going to the shallowest field - at one depth, to the only tagged one, else to none.
func jsonFields(t reflect.Type) []jsonField {
	var all []jsonField
	collectFields(t, 0, false, map[reflect.Type]bool{}, &all)

	byName := map[string][]jsonField{}
	var names []string
	for _, f := range all {
		if _, ok := byName[f.name]; !ok {
			names = append(names, f.name)
		}
		byName[f.name] = append(byName[f.name], f)
	}
	var out []jsonField
	for _, name := range names {
		if f, ok := dominant(byName[name]); ok {
			out = append(out, f)
		}
	}
	return out
}

func dominant(fields []jsonField) (jsonField, bool) {
	minDepth := fields[0].depth
	for _, f := range fields {
		minDepth = min(minDepth, f.depth)
	}
	var shallow []jsonField
	for _, f := range fields {
		if f.depth == minDepth {
			shallow = append(shallow, f)
		}
	}
	if len(shallow) == 1 {
		return shallow[0], true
	}
	var tagged []jsonField
	for _, f := range shallow {
		if f.tagged {
			tagged = append(tagged, f)
		}
	}
	if len(tagged) == 1 {
		return tagged[0], true
	}
	return jsonField{}, false
}

// collectFields gathers t's candidate fields. optional is set below an embedded pointer, whose
// promoted fields vanish when it is nil; seen stops an embedding cycle.
func collectFields(t reflect.Type, depth int, optional bool, seen map[reflect.Type]bool, out *[]jsonField) {
	if seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		ft := field.Type
		if field.Anonymous {
			embedded := ft
			throughPointer := false
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
				throughPointer = true
			}
			if !field.IsExported() && embedded.Kind() != reflect.Struct {
				continue
			}
			if name == "" && embedded.Kind() == reflect.Struct {
				collectFields(embedded, depth+1, optional || throughPointer, seen, out)
				continue
			}
		}
		if !field.IsExported() {
			continue
		}
		var omitempty, omitzero, quoted bool
		for _, opt := range strings.Split(opts, ",") {
			switch opt {
			case "omitempty":
				omitempty = true
			case "omitzero":
				omitzero = true
			case "string":
				quoted = quotable(ft)
			}
		}
		f := jsonField{name: name, typ: ft, depth: depth, tagged: name != "", quoted: quoted}
		if f.name == "" {
			f.name = field.Name
		}
		// omitempty never omits a struct, nor an array with elements; omitzero can omit anything.
		emptiable := ft.Kind() != reflect.Struct && !(ft.Kind() == reflect.Array && ft.Len() > 0)
		f.omittable = optional || omitzero || (omitempty && emptiable)
		*out = append(*out, f)
	}
}

// quotable reports whether the ",string" option applies to a field of type t: encoding/json honours
// it only on (pointers to) strings, booleans and numbers.
func quotable(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}
