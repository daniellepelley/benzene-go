package gengo

import (
	"strings"
	"testing"
)

func TestBuildTypeDecls_AllOfEmbedding(t *testing.T) {
	schemas := map[string]any{
		"Animal": schema("object", map[string]any{
			"properties": map[string]any{"name": schema("string", nil)},
		}),
		"Cat": schema("object", map[string]any{
			"allOf": []any{
				map[string]any{"$ref": "#/components/schemas/Animal"},
				schema("object", map[string]any{
					"properties": map[string]any{"livesLeft": schema("integer", nil)},
				}),
			},
		}),
	}

	decls, err := BuildTypeDecls(schemas)
	if err != nil {
		t.Fatalf("BuildTypeDecls: %v", err)
	}

	var catSrc string
	for _, d := range decls {
		if d.Name == "Cat" {
			catSrc = d.Source
		}
	}
	if catSrc == "" {
		t.Fatal("no Cat declaration generated")
	}
	if !containsAll(catSrc, "type Cat struct {", "Animal", "LivesLeft") {
		t.Errorf("Cat struct does not embed Animal / own field:\n%s", catSrc)
	}
}

func TestBuildTypeDecls_DiscriminatorUnion(t *testing.T) {
	schemas := map[string]any{
		"Base": schema("object", map[string]any{
			"properties": map[string]any{"kind": schema("string", nil)},
		}),
		"Cat": schema("object", map[string]any{
			"allOf": []any{map[string]any{"$ref": "#/components/schemas/Base"}},
		}),
		"Dog": schema("object", map[string]any{
			"allOf": []any{map[string]any{"$ref": "#/components/schemas/Base"}},
		}),
		"Pet": map[string]any{
			"oneOf": []any{
				map[string]any{"$ref": "#/components/schemas/Cat"},
				map[string]any{"$ref": "#/components/schemas/Dog"},
			},
			"discriminator": map[string]any{
				"propertyName": "kind",
				"mapping": map[string]any{
					"cat": "#/components/schemas/Cat",
					"dog": "#/components/schemas/Dog",
				},
			},
		},
	}

	decls, err := BuildTypeDecls(schemas)
	if err != nil {
		t.Fatalf("BuildTypeDecls: %v", err)
	}

	byName := map[string]TypeDecl{}
	for _, d := range decls {
		byName[d.Name] = d
	}

	pet, ok := byName["Pet"]
	if !ok {
		t.Fatal("no Pet declaration")
	}
	if !containsAll(pet.Source, "type Pet interface", "isPet()") {
		t.Errorf("Pet is not a marker interface:\n%s", pet.Source)
	}

	cat, ok := byName["Cat"]
	if !ok {
		t.Fatal("no Cat declaration")
	}
	if !containsAll(cat.Source, "func (Cat) isPet() {}") {
		t.Errorf("Cat does not implement isPet():\n%s", cat.Source)
	}

	dog := byName["Dog"]
	if !containsAll(dog.Source, "func (Dog) isPet() {}") {
		t.Errorf("Dog does not implement isPet():\n%s", dog.Source)
	}
}

func TestBuildTypeDecls_RequiredAndNullable(t *testing.T) {
	schemas := map[string]any{
		"Thing": schema("object", map[string]any{
			"required": []any{"id"},
			"properties": map[string]any{
				"id":       schema("string", nil),
				"nickname": schema("string", nil),
				"tags": schema("array", map[string]any{
					"items": schema("string", nil),
				}),
				"nullableRequired": schema("string", map[string]any{"nullable": true}),
			},
		}),
	}
	// "id" is required and non-nullable -> plain string, no omitempty.
	// "nickname" is optional -> *string, omitempty.
	// "tags" is optional but a slice -> []string (no pointer), omitempty.
	// "nullableRequired" would be required, but nullable:true overrides that to optional/pointer.
	schemas["Thing"].(map[string]any)["required"] = []any{"id", "nullableRequired"}

	decls, err := BuildTypeDecls(schemas)
	if err != nil {
		t.Fatalf("BuildTypeDecls: %v", err)
	}
	src := decls[0].Source
	if !containsAll(src,
		`Id string `+"`json:\"id\"`",
		`Nickname *string `+"`json:\"nickname,omitempty\"`",
		`Tags []string `+"`json:\"tags,omitempty\"`",
		`NullableRequired *string `+"`json:\"nullableRequired,omitempty\"`",
	) {
		t.Errorf("unexpected field rendering:\n%s", src)
	}
}

func TestLastPathSegment(t *testing.T) {
	cases := map[string]string{
		"#/components/schemas/Cat": "Cat",
		"Cat":                      "Cat",
		"":                         "",
	}
	for in, want := range cases {
		if got := lastPathSegment(in); got != want {
			t.Errorf("lastPathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOwnProperties_InlineAllOfDoesNotOverrideOwnKey(t *testing.T) {
	s := schema("object", map[string]any{
		"properties": map[string]any{"name": schema("string", nil)},
		"allOf": []any{
			schema("object", map[string]any{
				"properties": map[string]any{"name": schema("integer", nil)}, // shadowed
			}),
		},
	})
	props, _ := ownProperties(s)
	nameSchema, _ := props["name"].(map[string]any)
	if nameSchema["type"] != "string" {
		t.Errorf("own properties should win over inline allOf on key collision, got %v", nameSchema)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
