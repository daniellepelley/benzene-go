package gengo

// GoTypeName maps an OpenAPI 3.0 Schema Object (schema, as generic JSON: map[string]any) to a Go
// type name, mirroring the .NET reference's CSharpTypeName - adapted to Go's type system where
// it necessarily differs (documented at each such point below). catalogue is components.schemas,
// consulted only for a oneOf union site's shared-base lookup (see oneOfTypeName).
//
//   - A $ref names the referenced schema's own Go type (via FormatGoName - never the type
//     builder's own recursion, since the referenced type is generated as its own named struct).
//   - "array" -> "[]" + the item type.
//   - "string"+format "date-time" -> "time.Time" (stdlib only; the caller wraps it in a pointer
//     when the field is optional/nullable, like any other type - see BuildTypes).
//   - "string"+format "uuid" -> "string": Go has no built-in UUID type and this port does not
//     want to force a UUID library dependency onto every generated client, unlike .NET's
//     built-in Guid. A documented, deliberate divergence.
//   - "object" with a schema-valued additionalProperties -> "map[string]" + the value type; a
//     boolean additionalProperties (or none) falls through to the plain struct case.
//   - "integer": format "int64" -> "int64", else "int" (matching the reference's long/int split).
//   - "number" -> "float64" always, regardless of format - "inherit schema definition governs,
//     no decimal/format heuristics" per this port's parity ruling; matches the reference, which
//     also always emits "double" regardless of format.
//   - "boolean" -> "bool".
//   - a oneOf site with no $ref reference of its own -> oneOfTypeName.
//   - anything else (a bare "object" with no additionalProperties, e.g. an empty placeholder
//     type; an unset/empty type) -> the referencing $ref's own schema name, handled by the
//     struct builder, not here - see BuildTypes' handling of a schema with no distinguishing
//     shape.
func GoTypeName(schema map[string]any, catalogue map[string]any) string {
	if schema == nil {
		// The reference (CSharpTypeName.GetName) returns "Void" for a null schema, assuming a
		// "Void" placeholder type is registered in the catalogue - contract-document.md requires
		// requests[]/events[] request/response/message schemas to always be present, so this is
		// a defensive fallback, not a path real documents take.
		return "Void"
	}

	if ref, ok := schema["$ref"].(string); ok {
		if name := schemaRefName(ref); name != "" {
			return FormatGoName(name)
		}
	}

	schemaType, _ := schema["type"].(string)
	format, _ := schema["format"].(string)

	if schemaType == "array" {
		items, _ := schema["items"].(map[string]any)
		return "[]" + GoTypeName(items, catalogue)
	}

	if schemaType == "string" && format == "date-time" {
		return "time.Time"
	}
	if schemaType == "string" && format == "uuid" {
		return "string"
	}

	if schemaType == "object" {
		if ap, ok := schema["additionalProperties"].(map[string]any); ok {
			return "map[string]" + GoTypeName(ap, catalogue)
		}
	}

	if schemaType == "integer" {
		if format == "int64" {
			return "int64"
		}
		return "int"
	}

	if schemaType == "number" {
		return "float64"
	}

	if schemaType == "boolean" {
		return "bool"
	}

	if schemaType == "string" {
		return "string"
	}

	if oneOf, ok := schema["oneOf"].([]any); ok && len(oneOf) > 0 {
		return oneOfTypeName(oneOf, catalogue)
	}

	return "any"
}

const schemaRefPrefix = "#/components/schemas/"

func schemaRefName(ref string) string {
	if len(ref) <= len(schemaRefPrefix) || ref[:len(schemaRefPrefix)] != schemaRefPrefix {
		return ""
	}
	return ref[len(schemaRefPrefix):]
}

// oneOfTypeName mirrors the reference's oneOf-union-site fallback: when every member is a $ref
// and every referenced schema shares the same allOf base $ref, type the site as that shared base
// (the discriminator polymorphism case - see BuildTypes for the emitted interface); otherwise
// fall back to "any", the honest answer for a genuine union Go has no static type for.
func oneOfTypeName(oneOf []any, catalogue map[string]any) string {
	var sharedBase string
	for i, member := range oneOf {
		memberSchema, ok := member.(map[string]any)
		if !ok {
			return "any"
		}
		ref, ok := memberSchema["$ref"].(string)
		if !ok {
			return "any"
		}
		name := schemaRefName(ref)
		subtype, ok := catalogue[name].(map[string]any)
		if !ok {
			return "any"
		}
		base := allOfBaseRef(subtype)
		if base == "" {
			return "any"
		}
		if i == 0 {
			sharedBase = base
			continue
		}
		if base != sharedBase {
			return "any"
		}
	}
	if sharedBase == "" {
		return "any"
	}
	return FormatGoName(sharedBase)
}

// allOfBaseRef returns the schema name of the first allOf member that is itself a $ref (the
// "base type" branch, mirroring the reference's allOf-inheritance convention - see BuildTypes),
// or "" if schema has no such member.
func allOfBaseRef(schema map[string]any) string {
	allOf, ok := schema["allOf"].([]any)
	if !ok {
		return ""
	}
	for _, member := range allOf {
		memberSchema, ok := member.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := memberSchema["$ref"].(string); ok {
			return schemaRefName(ref)
		}
	}
	return ""
}
