package openapi_test

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
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

func createUser(_ context.Context, _ createUserReq) benzene.Result[createUserResp] {
	return benzene.Ok(createUserResp{})
}

func derive(t *testing.T, registry *benzene.Registry, info contract.Info) contract.Document {
	t.Helper()
	doc, err := contract.Derive(registry, contract.NewOutboundRegistry(), info)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return doc
}

func newDocument(t *testing.T) contract.Document {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("user:create"), benzene.Handler[createUserReq, createUserResp](createUser)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return derive(t, registry, contract.Info{Service: "users", ServiceVersion: "1.2.3", Description: "user intake"})
}

// component returns the named components schema of doc, failing the test when it is absent.
func component(t *testing.T, doc *openapi.Document, name string) map[string]any {
	t.Helper()
	if doc.Components == nil || doc.Components.Schemas[name] == nil {
		t.Fatalf("no component %q; components = %+v", name, doc.Components)
	}
	return doc.Components.Schemas[name]
}

func TestGenerate_InfoAndTopLevel(t *testing.T) {
	doc := openapi.Generate(newDocument(t))

	if doc.OpenAPI != "3.0.3" {
		t.Errorf("OpenAPI = %q, want 3.0.3", doc.OpenAPI)
	}
	if doc.Info.Title != "users" || doc.Info.Version != "1.2.3" || doc.Info.Description != "user intake" {
		t.Errorf("Info = %+v, want the document's service, version and description", doc.Info)
	}
}

func TestGenerate_RequestBecomesPostOperationReferencingComponents(t *testing.T) {
	doc := openapi.Generate(newDocument(t))

	item, ok := doc.Paths["/user:create"]
	if !ok || item.Post == nil {
		t.Fatalf("no POST /user:create; paths = %v", doc.Paths)
	}
	if item.Post.OperationID != "user_create" {
		t.Errorf("OperationID = %q, want user_create", item.Post.OperationID)
	}
	if item.Post.RequestBody == nil || !item.Post.RequestBody.Required {
		t.Fatal("expected a required request body")
	}
	schema := item.Post.RequestBody.Content["application/json"].Schema
	if schema["$ref"] != "#/components/schemas/createUserReq" {
		t.Fatalf("request schema = %v, want a $ref to the hoisted createUserReq", schema)
	}
	props := component(t, doc, "createUserReq")["properties"].(map[string]any)
	if got, _ := props["name"].(map[string]any); got["type"] != "string" {
		t.Errorf("name property = %v, want type string", props["name"])
	}
}

func TestGenerate_SchemasStayInTheOpenAPIDialect(t *testing.T) {
	doc := openapi.Generate(newDocument(t))
	props := component(t, doc, "createUserReq")["properties"].(map[string]any)

	// The document is already OpenAPI 3.0: a nullable scalar is {type, nullable: true}.
	nickname := props["nickname"].(map[string]any)
	if nickname["type"] != "string" || nickname["nullable"] != true {
		t.Errorf("nickname = %v, want {type:string, nullable:true}", nickname)
	}
	if tags := props["tags"].(map[string]any); tags["type"] != "array" || tags["items"].(map[string]any)["type"] != "string" {
		t.Errorf("tags = %v, want an array of strings", tags)
	}
	zip := component(t, doc, "address")["properties"].(map[string]any)["zip"].(map[string]any)
	if zip["type"] != "string" || zip["nullable"] != true {
		t.Errorf("address.zip = %v, want {type:string, nullable:true}", zip)
	}
}

func TestGenerate_DoesNotShareTheDocumentsMaps(t *testing.T) {
	source := newDocument(t)
	doc := openapi.Generate(source)

	component(t, doc, "createUserReq")["properties"].(map[string]any)["name"].(map[string]any)["type"] = "mutated"
	doc.Paths["/user:create"].Post.RequestBody.Content["application/json"].Schema["$ref"] = "mutated"
	if source.Components.Schemas["createUserReq"]["properties"].(map[string]any)["name"].(map[string]any)["type"] != "string" {
		t.Error("mutating the OpenAPI components changed the Contract Document")
	}
	if source.Requests[0].Request["$ref"] != "#/components/schemas/createUserReq" {
		t.Error("mutating an operation schema changed the Contract Document")
	}
}

func TestGenerate_Responses(t *testing.T) {
	doc := openapi.Generate(newDocument(t))
	responses := doc.Paths["/user:create"].Post.Responses

	success, ok := responses["200"]
	if !ok {
		t.Fatalf("no 200 response; responses = %v", responses)
	}
	if ref := success.Content["application/json"].Schema["$ref"]; ref != "#/components/schemas/createUserResp" {
		t.Errorf("200 response schema = %v, want a $ref to createUserResp", success.Content)
	}

	for _, code := range []string{"400", "401", "403", "404", "409", "429", "500", "503"} {
		resp, ok := responses[code]
		if !ok {
			t.Errorf("missing %s failure response; have %v", code, responses)
			continue
		}
		props, _ := resp.Content["application/json"].Schema["properties"].(map[string]any)
		if props["statusCode"] == nil || props["errors"] == nil {
			t.Errorf("%s response is not the error shape: %v", code, resp.Content)
		}
	}
}

func TestGenerate_MarshalsToValidJSON(t *testing.T) {
	doc := openapi.Generate(newDocument(t))
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("generated document is not valid JSON: %v", err)
	}
	if round["openapi"] != "3.0.3" || round["components"] == nil {
		t.Errorf("round-tripped = %v", round)
	}
	again, _ := json.Marshal(doc)
	if string(encoded) != string(again) {
		t.Error("output is not deterministic across marshals")
	}
}

func TestGenerate_ReadsAJSONRoundTrippedDocument(t *testing.T) {
	encoded, err := json.Marshal(newDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	var round contract.Document
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatal(err)
	}
	doc := openapi.Generate(round)
	required := component(t, doc, "createUserReq")["required"].([]any)
	if len(required) == 0 {
		t.Errorf("required = %v, want the always-present fields", required)
	}
}

func TestGenerate_Options(t *testing.T) {
	doc := openapi.Generate(newDocument(t),
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

func TestGenerate_DefaultsForEmptyDocument(t *testing.T) {
	doc := openapi.Generate(contract.Document{})
	if doc.Info.Title != "benzene-service" || doc.Info.Version != "0.0.0" {
		t.Errorf("Info = %+v, want the benzene-service/0.0.0 defaults", doc.Info)
	}
	if len(doc.Paths) != 0 || doc.Components != nil {
		t.Errorf("Paths = %v, Components = %v, want neither for an empty document", doc.Paths, doc.Components)
	}
}

func TestGenerate_TopicVersionInDescription(t *testing.T) {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("order:create").WithVersion("2"), benzene.Handler[createUserReq, createUserResp](createUser)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	doc := openapi.Generate(derive(t, registry, contract.Info{Service: "orders"}))

	if op := doc.Paths["/order:create"].Post; op == nil || op.Description != "Topic version 2" {
		t.Errorf("operation = %+v, want a topic-version description", op)
	}
}
