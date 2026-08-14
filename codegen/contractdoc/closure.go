package contractdoc

// ReachableSchemaNames walks roots (each an OpenAPI 3.0 Schema Object, or nil) against catalogue
// (components.schemas) and returns the set of catalogue names reachable from them, per
// contract-document.md §5.3: $ref, items, additionalProperties (only when itself a schema, not a
// bool), properties, and allOf/anyOf/oneOf. Cycle-safe - a name is only walked the first time it
// is reached, which is also what terminates a $ref cycle.
func ReachableSchemaNames(catalogue map[string]any, roots ...any) map[string]bool {
	reached := make(map[string]bool)
	for _, root := range roots {
		walkSchema(root, catalogue, reached)
	}
	return reached
}

// ReachableSchemas narrows catalogue down to exactly the entries ReachableSchemaNames reports
// reachable from roots, keyed the same as catalogue.
func ReachableSchemas(catalogue map[string]any, roots ...any) map[string]any {
	reached := ReachableSchemaNames(catalogue, roots...)
	out := make(map[string]any, len(reached))
	for name, schema := range catalogue {
		if reached[name] {
			out[name] = schema
		}
	}
	return out
}

func walkSchema(schema any, catalogue map[string]any, reached map[string]bool) {
	m, ok := schema.(map[string]any)
	if !ok || m == nil {
		return
	}

	if ref, ok := m["$ref"].(string); ok {
		if name := schemaRefName(ref); name != "" && !reached[name] {
			if target, exists := catalogue[name]; exists {
				reached[name] = true
				walkSchema(target, catalogue, reached)
			}
		}
	}

	if items, ok := m["items"]; ok {
		walkSchema(items, catalogue, reached)
	}

	// A boolean additionalProperties ("true"/"false") has nothing to walk - only a schema value
	// does.
	if ap, ok := m["additionalProperties"]; ok {
		if apSchema, ok := ap.(map[string]any); ok {
			walkSchema(apSchema, catalogue, reached)
		}
	}

	if props, ok := m["properties"].(map[string]any); ok {
		for _, propertySchema := range props {
			walkSchema(propertySchema, catalogue, reached)
		}
	}

	for _, key := range [...]string{"allOf", "anyOf", "oneOf"} {
		if members, ok := m[key].([]any); ok {
			for _, member := range members {
				walkSchema(member, catalogue, reached)
			}
		}
	}
}

const schemaRefPrefix = "#/components/schemas/"

// schemaRefName extracts <name> from a "#/components/schemas/<name>" JSON Pointer, the only
// $ref shape contract-document.md §4 permits. Returns "" for anything else.
func schemaRefName(ref string) string {
	if len(ref) <= len(schemaRefPrefix) || ref[:len(schemaRefPrefix)] != schemaRefPrefix {
		return ""
	}
	return ref[len(schemaRefPrefix):]
}
