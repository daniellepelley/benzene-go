package openapi_test

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/openapi"
)

type address struct {
	City string  `json:"city"`
	Zip  *string `json:"zip,omitempty"`
}

type createUserReq struct {
	Name     string   `json:"name"`
	Age      int      `json:"age"`
	Tags     []string `json:"tags,omitempty"`
	Address  address  `json:"address"`
	Nickname *string  `json:"nickname,omitempty"`
}

type createUserResp struct {
	ID string `json:"id"`
}

func newDescriptor(t *testing.T) mesh.Descriptor {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("user:create"),
		benzene.Handler[createUserReq, createUserResp](func(_ context.Context, _ createUserReq) benzene.Result[createUserResp] {
			return benzene.Ok(createUserResp{})
		})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return mesh.Describe(registry, mesh.ServiceInfo{Service: "users", ServiceVersion: "1.2.3"})
}

func TestGenerate_InfoAndTopLevel(t *testing.T) {
	doc := openapi.Generate(newDescriptor(t))

	if doc.OpenAPI != "3.0.3" {
		t.Errorf("OpenAPI = %q, want 3.0.3", doc.OpenAPI)
	}
	if doc.Info.Title != "users" {
		t.Errorf("Title = %q, want users (the service name)", doc.Info.Title)
	}
	if doc.Info.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3 (the service version)", doc.Info.Version)
	}
}

func TestGenerate_TopicBecomesPostOperation(t *testing.T) {
	doc := openapi.Generate(newDescriptor(t))

	item, ok := doc.Paths["/user:create"]
	if !ok {
		t.Fatalf("no path /user:create; paths = %v", doc.Paths)
	}
	if item.Post == nil {
		t.Fatal("path has no POST operation")
	}
	if item.Post.OperationID != "user_create" {
		t.Errorf("OperationID = %q, want user_create", item.Post.OperationID)
	}
	if item.Post.RequestBody == nil || !item.Post.RequestBody.Required {
		t.Fatal("expected a required request body")
	}
	schema := item.Post.RequestBody.Content["application/json"].Schema
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("request schema has no properties: %v", schema)
	}
	if got, _ := props["name"].(map[string]any); got["type"] != "string" {
		t.Errorf("name property = %v, want type string", props["name"])
	}
}

func TestGenerate_NullablePointerBecomesOpenAPINullable(t *testing.T) {
	doc := openapi.Generate(newDescriptor(t))
	schema := doc.Paths["/user:create"].Post.RequestBody.Content["application/json"].Schema
	props := schema["properties"].(map[string]any)

	nickname, ok := props["nickname"].(map[string]any)
	if !ok {
		t.Fatalf("no nickname property: %v", props)
	}
	if nickname["type"] != "string" {
		t.Errorf("nickname type = %v, want string (not a [string,null] array)", nickname["type"])
	}
	if nickname["nullable"] != true {
		t.Errorf("nickname nullable = %v, want true (OpenAPI 3.0 form)", nickname["nullable"])
	}
}

func TestGenerate_NestedSchemasRecurse(t *testing.T) {
	doc := openapi.Generate(newDescriptor(t))
	schema := doc.Paths["/user:create"].Post.RequestBody.Content["application/json"].Schema
	props := schema["properties"].(map[string]any)

	// array items recurse
	tags := props["tags"].(map[string]any)
	if tags["type"] != "array" {
		t.Errorf("tags type = %v, want array", tags["type"])
	}
	if items, _ := tags["items"].(map[string]any); items["type"] != "string" {
		t.Errorf("tags items = %v, want type string", tags["items"])
	}

	// nested struct's own nullable field is converted too
	addr := props["address"].(map[string]any)
	addrProps := addr["properties"].(map[string]any)
	zip := addrProps["zip"].(map[string]any)
	if zip["type"] != "string" || zip["nullable"] != true {
		t.Errorf("address.zip = %v, want {type:string, nullable:true}", zip)
	}
}

func TestGenerate_Responses(t *testing.T) {
	doc := openapi.Generate(newDescriptor(t))
	responses := doc.Paths["/user:create"].Post.Responses

	// 200 carries the response schema.
	success, ok := responses["200"]
	if !ok {
		t.Fatalf("no 200 response; responses = %v", responses)
	}
	respSchema := success.Content["application/json"].Schema
	if props, _ := respSchema["properties"].(map[string]any); props["id"] == nil {
		t.Errorf("200 response schema missing id property: %v", respSchema)
	}

	// A representative set of failure codes are documented with the error body shape.
	for _, code := range []string{"400", "401", "403", "404", "409", "429", "500", "503"} {
		resp, ok := responses[code]
		if !ok {
			t.Errorf("missing %s failure response; have %v", code, responses)
			continue
		}
		errSchema := resp.Content["application/json"].Schema
		props, _ := errSchema["properties"].(map[string]any)
		if props["statusCode"] == nil || props["errors"] == nil {
			t.Errorf("%s response is not the error shape: %v", code, errSchema)
		}
	}
}

func TestGenerate_MarshalsToValidJSON(t *testing.T) {
	doc := openapi.Generate(newDescriptor(t))
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("generated document is not valid JSON: %v", err)
	}
	if round["openapi"] != "3.0.3" {
		t.Errorf("round-tripped openapi = %v", round["openapi"])
	}
	// Determinism: marshaling twice yields byte-identical output (map keys sort).
	again, _ := json.Marshal(doc)
	if string(encoded) != string(again) {
		t.Error("output is not deterministic across marshals")
	}
}

func TestGenerate_NullableSurvivesJSONRoundTrippedDescriptor(t *testing.T) {
	// A descriptor that round-tripped through JSON has its type array as []any, not []string;
	// splitNullableType must handle that form too.
	desc := newDescriptor(t)
	encoded, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	var round mesh.Descriptor
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("unmarshal descriptor: %v", err)
	}

	doc := openapi.Generate(round)
	schema := doc.Paths["/user:create"].Post.RequestBody.Content["application/json"].Schema
	nickname := schema["properties"].(map[string]any)["nickname"].(map[string]any)
	if nickname["type"] != "string" || nickname["nullable"] != true {
		t.Errorf("nickname after JSON round-trip = %v, want {type:string, nullable:true}", nickname)
	}
}

func TestGenerate_Options(t *testing.T) {
	desc := newDescriptor(t)
	doc := openapi.Generate(desc,
		openapi.WithTitle("My API"),
		openapi.WithVersion("9.9.9"),
		openapi.WithDescription("hello"),
		openapi.WithPathPrefix("/api/"),
	)
	if doc.Info.Title != "My API" || doc.Info.Version != "9.9.9" || doc.Info.Description != "hello" {
		t.Errorf("options not applied: %+v", doc.Info)
	}
	if _, ok := doc.Paths["/api/user:create"]; !ok {
		t.Errorf("path prefix not applied; paths = %v", doc.Paths)
	}
}

func TestGenerate_DefaultsForEmptyDescriptor(t *testing.T) {
	doc := openapi.Generate(mesh.Descriptor{})
	if doc.Info.Title != "benzene-service" {
		t.Errorf("Title = %q, want the benzene-service default", doc.Info.Title)
	}
	if doc.Info.Version != "0.0.0" {
		t.Errorf("Version = %q, want the 0.0.0 default", doc.Info.Version)
	}
	if len(doc.Paths) != 0 {
		t.Errorf("Paths = %v, want empty for a topic-less descriptor", doc.Paths)
	}
}

func TestGenerate_TopicVersionInDescription(t *testing.T) {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("order:create").WithVersion("2"),
		benzene.Handler[createUserReq, createUserResp](func(_ context.Context, _ createUserReq) benzene.Result[createUserResp] {
			return benzene.Ok(createUserResp{})
		})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	desc := mesh.Describe(registry, mesh.ServiceInfo{Service: "orders"})
	doc := openapi.Generate(desc)

	var op *openapi.Operation
	for _, item := range doc.Paths {
		op = item.Post
	}
	if op == nil {
		t.Fatal("no operation generated")
	}
	if op.Description == "" {
		t.Errorf("expected a topic-version description, got empty (topic %q)", op.OperationID)
	}
}
