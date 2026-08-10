package openapi_test

import (
	"testing"

	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/openapi"
)

// TestGenerate_HandBuiltSchemaEdges drives Generate with a hand-built descriptor to exercise the
// schema-shape branches a normal mesh.Describe never emits but that a caller (or a degraded /
// externally-produced descriptor) could: a topic with no request/response schema, nullable type
// arrays in both list forms, non-nullable type arrays, and non-map nested values.
func TestGenerate_HandBuiltSchemaEdges(t *testing.T) {
	desc := mesh.Descriptor{
		Service: "edge",
		Topics: []mesh.TopicDescriptor{
			{ID: "no-body"}, // nil request + nil response schema
			{
				ID: "weird",
				RequestSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"threeTypes":   map[string]any{"type": []string{"a", "b", "c"}},       // len != 2 -> unchanged
						"nonStringArr": map[string]any{"type": []any{"string", 3}},            // []any non-string -> unchanged
						"neitherNull":  map[string]any{"type": []string{"string", "integer"}}, // 2 but no null -> unchanged
						"anyNull":      map[string]any{"type": []any{"null", "string"}},       // []any nullable, null first
						"strNull":      map[string]any{"type": []string{"integer", "null"}},   // []string nullable
					},
					"additionalProperties": true, // convertNested passthrough (non-map)
				},
			},
			{
				ID:            "props-not-map",
				RequestSchema: map[string]any{"type": "object", "properties": "oops"}, // properties not a map
			},
		},
	}

	// A prefix with neither a leading nor a trailing slash exercises both pathFor normalizations.
	doc := openapi.Generate(desc, openapi.WithPathPrefix("api"))

	noBody, ok := doc.Paths["/api/no-body"]
	if !ok {
		t.Fatalf("no /api/no-body path; paths = %v", doc.Paths)
	}
	if noBody.Post.RequestBody != nil {
		t.Error("no-body topic should have no request body")
	}
	if noBody.Post.Responses["200"].Content != nil {
		t.Error("no-body topic's 200 should have no content (nil response schema)")
	}

	weird := doc.Paths["/api/weird"].Post.RequestBody.Content["application/json"].Schema
	props := weird["properties"].(map[string]any)

	assertType := func(field string, want any) {
		got := props[field].(map[string]any)["type"]
		switch w := want.(type) {
		case string:
			if got != w {
				t.Errorf("%s type = %v, want %q", field, got, w)
			}
		}
	}
	// Non-nullable / malformed type arrays pass through unchanged (no "nullable" added).
	for _, field := range []string{"threeTypes", "nonStringArr", "neitherNull"} {
		if _, hasNullable := props[field].(map[string]any)["nullable"]; hasNullable {
			t.Errorf("%s should not have become nullable", field)
		}
	}
	// Genuine nullable arrays in both forms collapse to the non-null type + nullable:true.
	assertType("anyNull", "string")
	if props["anyNull"].(map[string]any)["nullable"] != true {
		t.Error("anyNull should be nullable")
	}
	assertType("strNull", "integer")
	if props["strNull"].(map[string]any)["nullable"] != true {
		t.Error("strNull should be nullable")
	}
	// Non-map additionalProperties passes through untouched.
	if weird["additionalProperties"] != true {
		t.Errorf("additionalProperties = %v, want true (passed through)", weird["additionalProperties"])
	}

	// properties that isn't a map is left as-is rather than crashing.
	notMap := doc.Paths["/api/props-not-map"].Post.RequestBody.Content["application/json"].Schema
	if notMap["properties"] != "oops" {
		t.Errorf("non-map properties = %v, want it passed through", notMap["properties"])
	}
}

// TestGenerate_OperationIDsUniqueAndNonEmpty covers the OpenAPI 3.0 operationId-uniqueness rule:
// distinct topic ids that sanitize to the same base get disambiguated, and a topic id with no
// alphanumerics falls back to a non-empty id rather than an (invalid) empty operationId.
func TestGenerate_OperationIDsUniqueAndNonEmpty(t *testing.T) {
	desc := mesh.Descriptor{Topics: []mesh.TopicDescriptor{
		{ID: "user:get"}, // -> user_get
		{ID: "user-get"}, // -> user_get (collision) -> user_get_2
		{ID: ":"},        // -> "" -> operation
		{ID: "::"},       // -> "" -> operation (collision) -> operation_2
	}}
	doc := openapi.Generate(desc)

	for _, p := range []string{"/user:get", "/user-get", "/:", "/::"} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("missing path %q; paths = %v", p, doc.Paths)
		}
	}

	seen := map[string]bool{}
	for path, item := range doc.Paths {
		id := item.Post.OperationID
		if id == "" {
			t.Errorf("empty operationId for path %q (invalid OpenAPI)", path)
		}
		if seen[id] {
			t.Errorf("duplicate operationId %q (violates OpenAPI 3.0 uniqueness)", id)
		}
		seen[id] = true
	}
	if id := doc.Paths["/user-get"].Post.OperationID; id != "user_get_2" {
		t.Errorf("collision id = %q, want user_get_2", id)
	}
	if id := doc.Paths["/::"].Post.OperationID; id != "operation_2" {
		t.Errorf("empty-base collision id = %q, want operation_2", id)
	}
}

// TestGenerate_EmptyPrefixNormalizes covers pathFor's empty-prefix branch (WithPathPrefix("")).
func TestGenerate_EmptyPrefixNormalizes(t *testing.T) {
	desc := mesh.Descriptor{Topics: []mesh.TopicDescriptor{{ID: "t"}}}
	doc := openapi.Generate(desc, openapi.WithPathPrefix(""))
	if _, ok := doc.Paths["/t"]; !ok {
		t.Errorf("empty prefix should normalize to root; paths = %v", doc.Paths)
	}
}
