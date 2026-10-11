package contract

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func serve(t *testing.T, mw benzene.Middleware, topic string, request any) (benzene.ResultInfo, bool) {
	t.Helper()
	ic := benzene.NewInvocationContext(benzene.NewTopic(topic), nil, request, nil)
	passed := false
	err := mw(context.Background(), ic, func(context.Context) error { passed = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return ic.Result, passed
}

func TestMiddleware_ServesTheDocument(t *testing.T) {
	doc := Document{SchemaDialect: SchemaDialect, Service: "s", Requests: []Request{}, Events: []Event{}, Sends: []Send{}}
	mw := Middleware(doc, WithDocumentType("AsyncAPI", "JSON", map[string]string{"asyncapi": "3.0.0"}))

	for name, request := range map[string]any{
		"nil": nil, "empty bytes": []byte{}, "blank raw": json.RawMessage("  "), "null": json.RawMessage("null"),
		"empty object": "{}", "explicit": json.RawMessage(`{"type":"benzene","format":"json"}`),
		"mixed case": json.RawMessage(`{"type":" Benzene ","format":"JSON"}`), "typed": SpecRequest{Type: "benzene"},
		"typed pointer": &SpecRequest{Format: "json"}, "nil pointer": (*SpecRequest)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			result, passed := serve(t, mw, TopicID, request)
			if passed || result == nil || result.ResultStatus() != benzene.StatusOk {
				t.Fatalf("result = %v, passed = %v", result, passed)
			}
			payload, _ := json.Marshal(result.ResultPayload())
			if !strings.Contains(string(payload), `"schemaDialect":"openapi-3.0"`) {
				t.Errorf("payload = %s", payload)
			}
		})
	}

	result, _ := serve(t, mw, TopicID, json.RawMessage(`{"type":"asyncapi","format":"json"}`))
	if payload, _ := json.Marshal(result.ResultPayload()); string(payload) != `{"asyncapi":"3.0.0"}` {
		t.Errorf("extension payload = %s", payload)
	}
}

func TestMiddleware_UnsupportedAndMalformedRequests(t *testing.T) {
	mw := Middleware(Document{Service: "s"})
	for request, want := range map[any]benzene.Status{
		`{"type":"openapi"}`:       benzene.StatusNotImplemented,
		`{"format":"yaml"}`:        benzene.StatusNotImplemented,
		`{"type":5}`:               benzene.StatusBadRequest,
		`[]`:                       benzene.StatusBadRequest,
		42:                         benzene.StatusBadRequest,
		`{"type":"asyncapi"}`:      benzene.StatusNotImplemented,
		`{"type":"benzene","x":1}`: benzene.StatusOk,
	} {
		result, _ := serve(t, mw, TopicID, request)
		if result.ResultStatus() != want {
			t.Errorf("%v: status = %s, want %s", request, result.ResultStatus(), want)
		}
	}
}

func TestMiddleware_PassesOtherTopicsOn(t *testing.T) {
	result, passed := serve(t, Middleware(Document{}), "orders:create", nil)
	if !passed || result != nil {
		t.Errorf("passed = %v, result = %v", passed, result)
	}
}

func TestMiddleware_PanicsOnAnUnmarshalablePayload(t *testing.T) {
	for name, build := range map[string]func(){
		"document":  func() { Middleware(Document{Requests: []Request{{Example: nanValue()}}}) },
		"extension": func() { WithDocumentType("x", "json", nanValue()) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected a start-up panic")
				}
			}()
			build()
		})
	}
}
