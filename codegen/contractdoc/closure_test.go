package contractdoc

import "testing"

func TestReachableSchemaNames_RefCycle(t *testing.T) {
	catalogue := map[string]any{
		"A": map[string]any{"type": "object", "properties": map[string]any{
			"b": map[string]any{"$ref": "#/components/schemas/B"},
		}},
		"B": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"$ref": "#/components/schemas/A"},
		}},
		"Unrelated": map[string]any{"type": "object"},
	}

	reached := ReachableSchemaNames(catalogue, map[string]any{"$ref": "#/components/schemas/A"})
	assertStringSlice(t, keysOf(reached), []string{"A", "B"})
}

func TestReachableSchemaNames_AllOfAnyOfOneOf(t *testing.T) {
	catalogue := map[string]any{
		"E": map[string]any{"allOf": []any{
			map[string]any{"$ref": "#/components/schemas/G"},
		}},
		"F": map[string]any{"type": "object"},
		"G": map[string]any{"type": "object"},
		"H": map[string]any{"type": "object"},
	}
	root := map[string]any{
		"oneOf": []any{
			map[string]any{"$ref": "#/components/schemas/E"},
			map[string]any{"$ref": "#/components/schemas/F"},
		},
		"anyOf": []any{
			map[string]any{"$ref": "#/components/schemas/H"},
		},
	}
	reached := ReachableSchemaNames(catalogue, root)
	assertStringSlice(t, keysOf(reached), []string{"E", "F", "G", "H"})
}

func TestReachableSchemaNames_ItemsAndAdditionalProperties(t *testing.T) {
	catalogue := map[string]any{
		"Item":       map[string]any{"type": "object"},
		"ListItem":   map[string]any{"type": "object"},
		"MapWrapper": map[string]any{"type": "object", "additionalProperties": map[string]any{"$ref": "#/components/schemas/Item"}},
	}
	root := map[string]any{
		"type":  "array",
		"items": map[string]any{"$ref": "#/components/schemas/ListItem"},
	}
	reached := ReachableSchemaNames(catalogue, root, map[string]any{"$ref": "#/components/schemas/MapWrapper"})
	assertStringSlice(t, keysOf(reached), []string{"Item", "ListItem", "MapWrapper"})
}

func TestReachableSchemaNames_BooleanAdditionalPropertiesNotWalked(t *testing.T) {
	catalogue := map[string]any{}
	root := map[string]any{"type": "object", "additionalProperties": true}
	reached := ReachableSchemaNames(catalogue, root)
	if len(reached) != 0 {
		t.Errorf("expected nothing reachable from a boolean additionalProperties, got %v", reached)
	}
}

func TestReachableSchemaNames_NilAndNonObjectRootsAreNoOps(t *testing.T) {
	catalogue := map[string]any{"A": map[string]any{"type": "object"}}
	reached := ReachableSchemaNames(catalogue, nil, "not a schema", 42)
	if len(reached) != 0 {
		t.Errorf("expected nothing reachable, got %v", reached)
	}
}

func TestReachableSchemaNames_DanglingRefIsIgnored(t *testing.T) {
	catalogue := map[string]any{}
	root := map[string]any{"$ref": "#/components/schemas/DoesNotExist"}
	reached := ReachableSchemaNames(catalogue, root)
	if len(reached) != 0 {
		t.Errorf("expected nothing reachable from a dangling ref, got %v", reached)
	}
}

func TestReachableSchemas(t *testing.T) {
	catalogue := map[string]any{
		"A":         map[string]any{"type": "object"},
		"Unrelated": map[string]any{"type": "object"},
	}
	root := map[string]any{"$ref": "#/components/schemas/A"}
	narrowed := ReachableSchemas(catalogue, root)
	if len(narrowed) != 1 {
		t.Fatalf("ReachableSchemas() = %v, want just A", narrowed)
	}
	if _, ok := narrowed["A"]; !ok {
		t.Error("ReachableSchemas() missing A")
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
