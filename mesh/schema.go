package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
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
)

// deriveSchema returns a JSON Schema (a documented subset of the 2020-12 vocabulary,
// mesh.md §9) describing how encoding/json marshals values of type t. It runs once per
// topic at startup, inside Describe - never on the invocation hot path - which is why
// stdlib reflect is acceptable here while dispatch remains reflection-free.
//
// The mapping (producer's view - it describes the marshaled form):
//
//   - strings -> "string"; bools -> "boolean"; integer kinds -> "integer"; floats -> "number"
//   - time.Time -> "string" with format "date-time"; types implementing
//     encoding.TextMarshaler -> "string"
//   - json.RawMessage, interfaces, and types implementing json.Marshaler (whose shape is
//     unknowable statically) -> {} (unconstrained)
//   - pointers -> the pointee's schema with "null" added to its type
//   - slices/arrays -> "array" with "items"; []byte -> "string" (base64, per encoding/json)
//   - maps -> "object" with "additionalProperties"
//   - structs -> "object" with "properties" from exported fields, honoring `json` tags
//     (name, "-", omitempty; ",string" maps to "string"); fields without omitempty are
//     listed in "required" (they are always present in the marshaled form). Embedded
//     structs are promoted like encoding/json promotes them, with two documented
//     simplifications: on a promoted-name conflict the first-seen field wins (rather than
//     encoding/json's full dominance rules), and fields promoted through a nil embedded
//     pointer are absent at runtime though the schema lists them.
//   - recursive types are cut at the cycle with {} rather than a $ref - a bounded,
//     honest approximation that keeps every schema self-contained
//   - kinds encoding/json cannot marshal at all (chan, func, complex, unsafe.Pointer) -> {}
//
// A nil type yields a nil schema (omitted from the descriptor), so a caller with no type
// information degrades to a schema-less catalog entry rather than an error.
func deriveSchema(t reflect.Type) map[string]any {
	if t == nil {
		return nil
	}
	return schemaFor(t, nil)
}

func schemaFor(t reflect.Type, visiting []reflect.Type) map[string]any {
	switch t {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case rawMessageType:
		return map[string]any{}
	}
	// A custom json.Marshaler makes the marshaled shape unknowable statically; a
	// TextMarshaler always marshals to a JSON string. Value-receiver methods only: that is
	// what encoding/json is guaranteed to invoke for the non-addressable values a handler
	// returns.
	if t.Implements(jsonMarshalerType) {
		return map[string]any{}
	}
	if t.Implements(textMarshalerType) {
		return map[string]any{"type": "string"}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Pointer:
		return nullable(schemaFor(t.Elem(), visiting))
	case reflect.Interface:
		return map[string]any{}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"} // encoding/json base64-encodes []byte
		}
		return map[string]any{"type": "array", "items": schemaFor(t.Elem(), visiting)}
	case reflect.Array:
		return map[string]any{"type": "array", "items": schemaFor(t.Elem(), visiting)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem(), visiting)}
	case reflect.Struct:
		for _, seen := range visiting {
			if seen == t {
				return map[string]any{} // cycle: cut with an unconstrained schema
			}
		}
		visiting = append(visiting, t)
		properties := map[string]any{}
		var required []string
		addStructFields(t, visiting, properties, &required)
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	return map[string]any{} // chan/func/complex/unsafe: not JSON-marshalable
}

// addStructFields collects t's exported fields into properties/required, promoting
// embedded structs the way encoding/json does (see deriveSchema's documented
// simplifications). required keeps field declaration order, which reflect guarantees is
// stable - determinism matters because the descriptor hash is computed over this output.
func addStructFields(t reflect.Type, visiting []reflect.Type, properties map[string]any, required *[]string) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, omitempty, asString := parseJSONTag(tag)

		if field.Anonymous && name == "" {
			embedded := field.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				addStructFields(embedded, visiting, properties, required)
				continue
			}
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if _, exists := properties[name]; exists {
			continue // promoted-name conflict: first seen wins
		}

		if asString {
			properties[name] = map[string]any{"type": "string"}
		} else {
			properties[name] = schemaFor(field.Type, visiting)
		}
		if !omitempty {
			*required = append(*required, name)
		}
	}
}

func parseJSONTag(tag string) (name string, omitempty, asString bool) {
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		switch opt {
		case "omitempty":
			omitempty = true
		case "string":
			asString = true
		}
	}
	return name, omitempty, asString
}

// nullable adds "null" to a schema's type, for pointer fields. An unconstrained schema
// ({}) already admits null, and a type that is already a list (pointer-to-pointer) is
// already nullable - both pass through unchanged.
func nullable(schema map[string]any) map[string]any {
	if typ, ok := schema["type"].(string); ok {
		schema["type"] = []string{typ, "null"}
	}
	return schema
}

// descriptorHash computes the contract hash of mesh.md §5.1: sha256 over the descriptor's
// canonical JSON with the per-instance and transient fields blanked - InstanceID
// identifies a copy of the service, not its contract, and Degraded reflects feed
// availability at build time - so two instances of the same build hash identically, and
// the hash changes exactly when the contract (identity, placement, topics, schemas)
// changes. Canonical: struct fields marshal in declaration order and Go maps marshal with
// sorted keys, so equal descriptors yield byte-equal JSON.
func descriptorHash(desc Descriptor) string {
	desc.InstanceID = ""
	desc.DescriptorHash = ""
	desc.Degraded = nil
	// Marshal cannot fail: the descriptor is strings plus schema maps of
	// strings/slices/maps produced by deriveSchema.
	data, _ := json.Marshal(desc)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
