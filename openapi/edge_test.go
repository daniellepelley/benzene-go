package openapi_test

import (
	"testing"

	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/openapi"
)

// TestGenerate_HandBuiltDocumentEdges drives Generate with a hand-built document - one a non-Go
// producer, or a JSON round trip, could hand it: an entry with neither request nor response schema,
// a reserved entry (which a consumer ignores), and slices of every JSON kind inside a schema.
func TestGenerate_HandBuiltDocumentEdges(t *testing.T) {
	doc := openapi.Generate(contract.Document{
		Service: "edge",
		Requests: []contract.Request{
			{Topic: "no-body"},
			{Topic: "benzene:spec", Request: contract.Schema{}},
			{Topic: "enum", Request: contract.Schema{
				"type": "string", "enum": []any{"a", "b"},
				"allOf": []any{map[string]any{"required": []string{"x"}}},
			}},
		},
	}, openapi.WithPathPrefix("api")) // neither a leading nor a trailing slash: both normalizations

	noBody, ok := doc.Paths["/api/no-body"]
	if !ok {
		t.Fatalf("no /api/no-body path; paths = %v", doc.Paths)
	}
	if noBody.Post.RequestBody != nil || noBody.Post.Responses["200"].Content != nil {
		t.Error("an entry without schemas should have no request body and no 200 content")
	}
	if _, ok := doc.Paths["/api/benzene:spec"]; ok {
		t.Error("a reserved entry became an operation")
	}
	enum := doc.Paths["/api/enum"].Post.RequestBody.Content["application/json"].Schema
	if len(enum["enum"].([]any)) != 2 || enum["allOf"].([]any)[0].(map[string]any)["required"].([]string)[0] != "x" {
		t.Errorf("enum schema = %v, want its slices copied intact", enum)
	}
}

// TestGenerate_OperationIDsUniqueAndNonEmpty covers the OpenAPI 3.0 operationId-uniqueness rule:
// distinct topic ids that sanitize to the same base get disambiguated, and a topic id with no
// alphanumerics falls back to a non-empty id rather than an (invalid) empty operationId.
func TestGenerate_OperationIDsUniqueAndNonEmpty(t *testing.T) {
	doc := openapi.Generate(contract.Document{Requests: []contract.Request{
		{Topic: "user:get"}, // -> user_get
		{Topic: "user-get"}, // -> user_get (collision) -> user_get_2
		{Topic: ":"},        // -> "" -> operation
		{Topic: "::"},       // -> "" -> operation (collision) -> operation_2
	}})

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
	doc := openapi.Generate(contract.Document{Requests: []contract.Request{{Topic: "t"}}}, openapi.WithPathPrefix(""))
	if _, ok := doc.Paths["/t"]; !ok {
		t.Errorf("empty prefix should normalize to root; paths = %v", doc.Paths)
	}
}
