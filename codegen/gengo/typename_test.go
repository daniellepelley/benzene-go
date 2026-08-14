package gengo

import "testing"

func schema(t string, extra map[string]any) map[string]any {
	m := map[string]any{}
	if t != "" {
		m["type"] = t
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestGoTypeName(t *testing.T) {
	catalogue := map[string]any{
		"Widget": schema("object", nil),
	}

	cases := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{"nil schema", nil, "Void"},
		{"ref", map[string]any{"$ref": "#/components/schemas/Widget"}, "Widget"},
		{"string", schema("string", nil), "string"},
		{"date-time", schema("string", map[string]any{"format": "date-time"}), "time.Time"},
		{"uuid", schema("string", map[string]any{"format": "uuid"}), "string"},
		{"int32", schema("integer", nil), "int"},
		{"int64", schema("integer", map[string]any{"format": "int64"}), "int64"},
		{"number", schema("number", map[string]any{"format": "float"}), "float64"},
		{"boolean", schema("boolean", nil), "bool"},
		{"array of ref", schema("array", map[string]any{
			"items": map[string]any{"$ref": "#/components/schemas/Widget"},
		}), "[]Widget"},
		{"array of string", schema("array", map[string]any{
			"items": schema("string", nil),
		}), "[]string"},
		{"map of string", schema("object", map[string]any{
			"additionalProperties": schema("string", nil),
		}), "map[string]string"},
		{"map of ref", schema("object", map[string]any{
			"additionalProperties": map[string]any{"$ref": "#/components/schemas/Widget"},
		}), "map[string]Widget"},
		{"bare object (no additionalProperties)", schema("object", nil), "any"},
		{"unrecognized shape", map[string]any{}, "any"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := GoTypeName(c.schema, catalogue); got != c.want {
				t.Errorf("GoTypeName(%v) = %q, want %q", c.schema, got, c.want)
			}
		})
	}
}

func TestGoTypeName_OneOfSharedBase(t *testing.T) {
	catalogue := map[string]any{
		"Base": schema("object", nil),
		"Cat": schema("object", map[string]any{
			"allOf": []any{
				map[string]any{"$ref": "#/components/schemas/Base"},
				schema("object", map[string]any{"properties": map[string]any{"meow": schema("boolean", nil)}}),
			},
		}),
		"Dog": schema("object", map[string]any{
			"allOf": []any{
				map[string]any{"$ref": "#/components/schemas/Base"},
			},
		}),
	}

	oneOfSite := []any{
		map[string]any{"$ref": "#/components/schemas/Cat"},
		map[string]any{"$ref": "#/components/schemas/Dog"},
	}

	got := oneOfTypeName(oneOfSite, catalogue)
	if got != "Base" {
		t.Errorf("oneOfTypeName shared base = %q, want %q", got, "Base")
	}
}

func TestGoTypeName_OneOfNoSharedBaseFallsBackToAny(t *testing.T) {
	catalogue := map[string]any{
		"Foo": schema("object", nil), // no allOf -> no base
		"Bar": schema("object", nil),
	}
	oneOfSite := []any{
		map[string]any{"$ref": "#/components/schemas/Foo"},
		map[string]any{"$ref": "#/components/schemas/Bar"},
	}
	if got := oneOfTypeName(oneOfSite, catalogue); got != "any" {
		t.Errorf("oneOfTypeName = %q, want any", got)
	}

	// A oneOf member that is itself inline (no $ref) also falls back to "any".
	inlineMember := []any{schema("object", nil)}
	if got := oneOfTypeName(inlineMember, catalogue); got != "any" {
		t.Errorf("oneOfTypeName(inline member) = %q, want any", got)
	}

	// A oneOf member $ref'ing an unknown catalogue entry falls back to "any".
	unknownMember := []any{map[string]any{"$ref": "#/components/schemas/DoesNotExist"}}
	if got := oneOfTypeName(unknownMember, catalogue); got != "any" {
		t.Errorf("oneOfTypeName(unknown ref) = %q, want any", got)
	}
}

func TestSchemaRefName(t *testing.T) {
	cases := map[string]string{
		"#/components/schemas/Foo": "Foo",
		"#/components/schemas/":    "",
		"not-a-ref":                "",
		"":                         "",
	}
	for ref, want := range cases {
		if got := schemaRefName(ref); got != want {
			t.Errorf("schemaRefName(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestAllOfBaseRef(t *testing.T) {
	if got := allOfBaseRef(map[string]any{}); got != "" {
		t.Errorf("no allOf: got %q, want empty", got)
	}
	withBase := map[string]any{
		"allOf": []any{
			schema("object", map[string]any{"properties": map[string]any{}}), // inline, skipped
			map[string]any{"$ref": "#/components/schemas/Base"},
		},
	}
	if got := allOfBaseRef(withBase); got != "Base" {
		t.Errorf("allOfBaseRef = %q, want Base", got)
	}
}
