package asyncapi_test

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/asyncapi"
	"github.com/daniellepelley/benzene-go/mesh"
)

type createOrderReq struct {
	ID   string  `json:"id"`
	Note *string `json:"note,omitempty"` // nullable -> mesh derives ["string","null"]
}

type createOrderResp struct {
	ID string `json:"id"`
}

func newDescriptor(t *testing.T, topics ...string) mesh.Descriptor {
	t.Helper()
	if len(topics) == 0 {
		topics = []string{"order:create"}
	}
	registry := benzene.NewRegistry()
	for _, topic := range topics {
		if err := benzene.Register(registry, benzene.NewTopic(topic),
			benzene.Handler[createOrderReq, createOrderResp](func(_ context.Context, _ createOrderReq) benzene.Result[createOrderResp] {
				return benzene.Ok(createOrderResp{})
			})); err != nil {
			t.Fatalf("Register(%s): %v", topic, err)
		}
	}
	return mesh.Describe(registry, mesh.ServiceInfo{Service: "orders", ServiceVersion: "1.2.3"})
}

func TestGenerate_TopLevelAndInfo(t *testing.T) {
	doc := asyncapi.Generate(newDescriptor(t))

	if doc.AsyncAPI != "3.0.0" {
		t.Errorf("asyncapi = %q, want 3.0.0", doc.AsyncAPI)
	}
	if doc.ID != "urn:benzene:service:orders" {
		t.Errorf("id = %q, want urn:benzene:service:orders", doc.ID)
	}
	if doc.DefaultContentType != "application/json" {
		t.Errorf("defaultContentType = %q", doc.DefaultContentType)
	}
	if doc.Info.Title != "orders" || doc.Info.Version != "1.2.3" {
		t.Errorf("info = %+v, want orders/1.2.3", doc.Info)
	}
}

func TestGenerate_HandledTopicIsReceiveWithReply(t *testing.T) {
	doc := asyncapi.Generate(newDescriptor(t))

	req, ok := doc.Channels["order:create"]
	if !ok {
		t.Fatalf("no request channel order:create; channels=%v", doc.Channels)
	}
	if req.Address != "order:create" {
		t.Errorf("address = %q", req.Address)
	}
	if _, ok := req.Messages["request"]; !ok {
		t.Errorf("request channel has no request message: %+v", req.Messages)
	}
	// The request payload carries the derived schema (an object with an id property).
	payload := req.Messages["request"].Payload
	if payload["type"] != "object" {
		t.Errorf("request payload type = %v, want object", payload["type"])
	}

	reply, ok := doc.Channels["order:create:response"]
	if !ok {
		t.Fatalf("no reply channel order:create:response; channels=%v", doc.Channels)
	}
	if reply.Address != "order:create:response" {
		t.Errorf("reply address = %q", reply.Address)
	}

	op, ok := doc.Operations["receive_order_create"]
	if !ok {
		t.Fatalf("no receive operation; operations=%v", doc.Operations)
	}
	if op.Action != "receive" {
		t.Errorf("action = %q, want receive", op.Action)
	}
	if op.Channel.Ref != "#/channels/order:create" {
		t.Errorf("channel ref = %q", op.Channel.Ref)
	}
	if op.Reply == nil || op.Reply.Channel.Ref != "#/channels/order:create:response" {
		t.Errorf("reply ref = %+v", op.Reply)
	}
}

func TestGenerate_NullableSchemaPassesThrough(t *testing.T) {
	// AsyncAPI 3.0 is JSON Schema, so mesh's nullable type array survives with no reshaping.
	doc := asyncapi.Generate(newDescriptor(t))
	props := doc.Channels["order:create"].Messages["request"].Payload["properties"].(map[string]any)
	note := props["note"].(map[string]any)
	types, ok := note["type"].([]string)
	if !ok || len(types) != 2 {
		t.Fatalf("note type = %v (%T), want a nullable [\"string\",\"null\"] array", note["type"], note["type"])
	}
}

func TestGenerate_SentEvent(t *testing.T) {
	eventPayload := map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"examples":   []any{"a", "b"}, // exercises copyValue's []any branch
	}
	doc := asyncapi.Generate(newDescriptor(t), asyncapi.WithSentEvent("order:created", eventPayload))

	ch, ok := doc.Channels["order:created"]
	if !ok {
		t.Fatalf("no sent-event channel; channels=%v", doc.Channels)
	}
	if _, ok := ch.Messages["event"]; !ok {
		t.Errorf("sent channel has no event message")
	}
	op, ok := doc.Operations["send_order_created"]
	if !ok || op.Action != "send" {
		t.Fatalf("no send operation; operations=%v", doc.Operations)
	}
	if op.Reply != nil {
		t.Error("a send operation should have no reply")
	}
	// Deep copy: mutating the input payload afterward must not affect the document.
	eventPayload["type"] = "MUTATED"
	if doc.Channels["order:created"].Messages["event"].Payload["type"] != "object" {
		t.Error("copySchema did not deep-copy the sent-event payload")
	}
}

func TestGenerate_SentEventReplaceAndNilPayload(t *testing.T) {
	doc := asyncapi.Generate(newDescriptor(t),
		asyncapi.WithSentEvent("order:created", map[string]any{"type": "object"}),
		asyncapi.WithSentEvent("order:created", nil), // replaces the earlier declaration
	)
	// Only one channel/operation for the topic, and its payload is the replacement (nil).
	if doc.Channels["order:created"].Messages["event"].Payload != nil {
		t.Errorf("payload = %v, want nil after replacement", doc.Channels["order:created"].Messages["event"].Payload)
	}
	count := 0
	for key := range doc.Operations {
		if key == "send_order_created" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("send operations for order:created = %d, want 1", count)
	}
}

func TestGenerate_Options(t *testing.T) {
	doc := asyncapi.Generate(newDescriptor(t),
		asyncapi.WithTitle("Custom"),
		asyncapi.WithVersion("9.9.9"),
		asyncapi.WithDescription("a service"),
		asyncapi.WithResponseTopicSuffix("reply"),
	)
	if doc.Info.Title != "Custom" || doc.Info.Version != "9.9.9" || doc.Info.Description != "a service" {
		t.Errorf("info = %+v", doc.Info)
	}
	if _, ok := doc.Channels["order:create:reply"]; !ok {
		t.Errorf("custom reply suffix not applied; channels=%v", doc.Channels)
	}
	// An empty suffix is ignored (the default is kept).
	def := asyncapi.Generate(newDescriptor(t), asyncapi.WithResponseTopicSuffix("   "))
	if _, ok := def.Channels["order:create:response"]; !ok {
		t.Error("empty suffix should have kept the default 'response'")
	}
}

func TestGenerate_DefaultsForEmptyServiceIdentity(t *testing.T) {
	registry := benzene.NewRegistry()
	_ = benzene.Register(registry, benzene.NewTopic("t:do"),
		benzene.Handler[createOrderReq, createOrderResp](func(_ context.Context, _ createOrderReq) benzene.Result[createOrderResp] {
			return benzene.Ok(createOrderResp{})
		}))
	desc := mesh.Describe(registry, mesh.ServiceInfo{}) // no service name/version

	doc := asyncapi.Generate(desc)
	if doc.Info.Title != "benzene-service" || doc.Info.Version != "0.0.0" {
		t.Errorf("defaults not applied: %+v", doc.Info)
	}
	// A title with no alphanumerics falls back to the bare urn.
	blank := asyncapi.Generate(desc, asyncapi.WithTitle("!!!"))
	if blank.ID != "urn:benzene:service" {
		t.Errorf("id = %q, want the bare urn fallback", blank.ID)
	}
}

func TestGenerate_OperationKeyCollisionDisambiguated(t *testing.T) {
	// "a:b" and "a-b" both slug to "a_b"; the second receive operation gets a _2 suffix.
	doc := asyncapi.Generate(newDescriptor(t, "a:b", "a-b"))
	if _, ok := doc.Operations["receive_a_b"]; !ok {
		t.Error("missing receive_a_b")
	}
	if _, ok := doc.Operations["receive_a_b_2"]; !ok {
		t.Errorf("collision not disambiguated; operations=%v", doc.Operations)
	}
}

func TestGenerate_MarshalsToValidJSON(t *testing.T) {
	doc := asyncapi.Generate(newDescriptor(t), asyncapi.WithSentEvent("order:created", nil))
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round["asyncapi"] != "3.0.0" {
		t.Errorf("round-tripped asyncapi = %v", round["asyncapi"])
	}
}
