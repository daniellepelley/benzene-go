package asyncapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/asyncapi"
	"github.com/daniellepelley/benzene-go/contract"
)

type createOrderReq struct {
	ID   string  `json:"id"`
	Note *string `json:"note,omitempty"` // nullable -> derived {"type":"string","nullable":true}
}

type createOrderResp struct {
	ID string `json:"id"`
}

type orderCreated struct {
	ID       string           `json:"id"`
	Previous *createOrderResp `json:"previous"` // a nullable named type -> the nullable wrapper
}

type outboundDecl func(*contract.OutboundRegistry)

func newDocument(t *testing.T, topics []string, outbound ...outboundDecl) contract.Document {
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
	declared := contract.NewOutboundRegistry()
	for _, declare := range outbound {
		declare(declared)
	}
	doc, err := contract.Derive(registry, declared, contract.Info{Service: "orders", ServiceVersion: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func event(topic string) outboundDecl {
	return func(r *contract.OutboundRegistry) {
		contract.MustRegisterEvent[orderCreated](r, benzene.NewTopic(topic))
	}
}

func TestGenerate_TopLevelAndInfo(t *testing.T) {
	doc := asyncapi.Generate(newDocument(t, nil))

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
	doc := asyncapi.Generate(newDocument(t, nil))

	// Channel MAP KEYS are sanitized (AsyncAPI 3.0 wants [A-Za-z0-9._-]); the raw ':' topic lives in
	// the channel's address.
	req, ok := doc.Channels["order_create"]
	if !ok {
		t.Fatalf("no request channel keyed order_create; channels=%v", doc.Channels)
	}
	if req.Address != "order:create" {
		t.Errorf("address = %q, want the raw topic order:create", req.Address)
	}
	if _, ok := req.Messages["request"]; !ok {
		t.Errorf("request channel has no request message: %+v", req.Messages)
	}
	if ref := req.Messages["request"].Payload["$ref"]; ref != "#/components/schemas/createOrderReq" {
		t.Errorf("request payload = %v, want a $ref to the createOrderReq component", req.Messages["request"].Payload)
	}

	reply, ok := doc.Channels["order_create_response"]
	if !ok {
		t.Fatalf("no reply channel keyed order_create_response; channels=%v", doc.Channels)
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
	if op.Channel.Ref != "#/channels/order_create" {
		t.Errorf("channel ref = %q", op.Channel.Ref)
	}
	if op.Reply == nil || op.Reply.Channel.Ref != "#/channels/order_create_response" {
		t.Errorf("reply ref = %+v", op.Reply)
	}
}

func TestGenerate_HandledAndSentTopicMergeIntoOneChannel(t *testing.T) {
	// A topic that is both handled AND declared as an event must become ONE channel carrying both the
	// request and the event message (find-by-address-add-message), not a duplicate or overwrite.
	doc := asyncapi.Generate(newDocument(t, nil, event("order:create")))

	ch, ok := doc.Channels["order_create"]
	if !ok {
		t.Fatalf("no order_create channel; channels=%v", doc.Channels)
	}
	if _, ok := ch.Messages["request"]; !ok {
		t.Error("merged channel lost its request message")
	}
	if _, ok := ch.Messages["event"]; !ok {
		t.Error("merged channel lost its event message")
	}
	// Both operations reference the same single channel.
	if doc.Operations["receive_order_create"].Channel.Ref != "#/channels/order_create" ||
		doc.Operations["send_order_create"].Channel.Ref != "#/channels/order_create" {
		t.Error("receive and send operations should both reference #/channels/order_create")
	}
}

func TestGenerate_NullabilityBecomesJSONSchema(t *testing.T) {
	// AsyncAPI 3.0's default schema format is JSON Schema: OpenAPI 3.0's nullable is rewritten.
	doc := asyncapi.Generate(newDocument(t, nil, event("order:created")))
	schemas := doc.Components.Schemas
	note := schemas["createOrderReq"]["properties"].(map[string]any)["note"].(map[string]any)
	if types, ok := note["type"].([]string); !ok || len(types) != 2 || types[1] != "null" || note["nullable"] != nil {
		t.Errorf("note = %v, want type [\"string\",\"null\"] and no nullable", note)
	}
	previous := schemas["orderCreated"]["properties"].(map[string]any)["previous"].(map[string]any)
	anyOf, ok := previous["anyOf"].([]any)
	if !ok || len(anyOf) != 2 || previous["allOf"] != nil || previous["type"] != nil {
		t.Fatalf("previous = %v, want anyOf [$ref, {type: null}]", previous)
	}
	if anyOf[0].(map[string]any)["$ref"] != "#/components/schemas/createOrderResp" {
		t.Errorf("previous anyOf[0] = %v", anyOf[0])
	}
}

func TestGenerate_EventsAndSends(t *testing.T) {
	src := newDocument(t, nil, event("order:created"),
		func(r *contract.OutboundRegistry) {
			contract.MustRegisterSend[createOrderReq](r, benzene.NewTopic("payment:take"))
			contract.MustRegisterSend[createOrderReq](r, benzene.NewTopic("stock:reserve"), contract.WithResponse[createOrderResp]())
		})
	// Reserved entries are never listed by a producer, but a hand-built document may carry them.
	src.Requests = append(src.Requests, contract.Request{Topic: "benzene:spec", Request: contract.Schema{}})
	src.Events = append(src.Events, contract.Event{Topic: "benzene:x", Message: contract.Schema{}})
	src.Sends = append(src.Sends, contract.Send{Topic: "benzene:y", Request: contract.Schema{}})
	doc := asyncapi.Generate(src)

	if op := doc.Operations["send_order_created"]; op.Action != "send" || op.Reply != nil {
		t.Errorf("event operation = %+v, want a send with no reply", op)
	}
	if _, ok := doc.Channels["order_created"].Messages["event"]; !ok {
		t.Error("event channel has no event message")
	}
	if op := doc.Operations["send_payment_take"]; op.Action != "send" || op.Reply != nil {
		t.Errorf("one-way send = %+v, want no reply", op)
	}
	if op := doc.Operations["send_stock_reserve"]; op.Reply == nil || op.Reply.Channel.Ref != "#/channels/stock_reserve_response" {
		t.Errorf("send with a reply = %+v", op)
	}
	for key := range doc.Operations {
		if strings.Contains(key, "benzene") {
			t.Errorf("reserved entry became operation %s", key)
		}
	}
}

func TestGenerate_NoResponseMeansNoReply(t *testing.T) {
	registry := benzene.NewRegistry()
	benzene.MustRegister(registry, benzene.NewTopic("order:void"), func(_ context.Context, _ createOrderReq) benzene.Result[struct{}] {
		return benzene.Ok(struct{}{})
	})
	src, err := contract.Derive(registry, nil, contract.Info{Service: "orders", Description: "from the document"})
	if err != nil {
		t.Fatal(err)
	}
	doc := asyncapi.Generate(src)
	if op := doc.Operations["receive_order_void"]; op.Action != "receive" || op.Reply != nil {
		t.Errorf("operation = %+v, want a receive with no reply", op)
	}
	if doc.Info.Description != "from the document" {
		t.Errorf("description = %q", doc.Info.Description)
	}
}

func TestGenerate_DoesNotMutateTheDocument(t *testing.T) {
	src := newDocument(t, nil)
	before, _ := json.Marshal(src)
	asyncapi.Generate(src)
	if after, _ := json.Marshal(src); string(after) != string(before) {
		t.Errorf("Generate mutated the document:\n%s\n%s", before, after)
	}
}

func TestGenerate_Options(t *testing.T) {
	doc := asyncapi.Generate(newDocument(t, nil),
		asyncapi.WithTitle("Custom"),
		asyncapi.WithVersion("9.9.9"),
		asyncapi.WithDescription("a service"),
		asyncapi.WithResponseTopicSuffix("reply"),
	)
	if doc.Info.Title != "Custom" || doc.Info.Version != "9.9.9" || doc.Info.Description != "a service" {
		t.Errorf("info = %+v", doc.Info)
	}
	if _, ok := doc.Channels["order_create_reply"]; !ok {
		t.Errorf("custom reply suffix not applied; channels=%v", doc.Channels)
	}
	// An empty suffix is ignored (the default is kept).
	def := asyncapi.Generate(newDocument(t, nil), asyncapi.WithResponseTopicSuffix("   "))
	if _, ok := def.Channels["order_create_response"]; !ok {
		t.Error("empty suffix should have kept the default 'response'")
	}
}

func TestGenerate_DefaultsForEmptyServiceIdentity(t *testing.T) {
	src := contract.Document{Requests: []contract.Request{{Topic: "t:do", Request: contract.Schema{"type": "object"}}}} // no service name/version

	doc := asyncapi.Generate(src)
	if doc.Info.Title != "benzene-service" || doc.Info.Version != "0.0.0" {
		t.Errorf("defaults not applied: %+v", doc.Info)
	}
	if doc.Components != nil {
		t.Errorf("components = %+v, want none for a document without named schemas", doc.Components)
	}
	// A title with no alphanumerics falls back to the bare urn.
	blank := asyncapi.Generate(src, asyncapi.WithTitle("!!!"))
	if blank.ID != "urn:benzene:service" {
		t.Errorf("id = %q, want the bare urn fallback", blank.ID)
	}
	// The urn slug uses '-' (not '_'), matching the .NET builder, so a service gets the same id
	// across ports.
	spaced := asyncapi.Generate(src, asyncapi.WithTitle("Order Service"))
	if spaced.ID != "urn:benzene:service:order-service" {
		t.Errorf("id = %q, want urn:benzene:service:order-service", spaced.ID)
	}
}

func TestGenerate_KeyCollisionDisambiguated(t *testing.T) {
	// "a:b" and "a-b" both slug to "a_b" for BOTH the operation key and the channel key; the second of
	// each gets a _2 suffix, and the two distinct addresses stay on distinct channels.
	doc := asyncapi.Generate(newDocument(t, []string{"a:b", "a-b"}))

	if _, ok := doc.Operations["receive_a_b"]; !ok {
		t.Error("missing receive_a_b")
	}
	if _, ok := doc.Operations["receive_a_b_2"]; !ok {
		t.Errorf("operation collision not disambiguated; operations=%v", doc.Operations)
	}
	// Distinct addresses must not share a channel: the second gets a _2-suffixed channel key.
	first, ok1 := doc.Channels["a_b"]
	second, ok2 := doc.Channels["a_b_2"]
	if !ok1 || !ok2 {
		t.Fatalf("channel keys not disambiguated; channels=%v", doc.Channels)
	}
	if first.Address == second.Address {
		t.Errorf("two channels share address %q; they should be distinct", first.Address)
	}
}

func TestGenerate_AllPunctuationTopicGetsChannelFallback(t *testing.T) {
	// An event topic with no alphanumerics slugs to "" and falls back to the "channel" key.
	doc := asyncapi.Generate(newDocument(t, nil, event(":::")))
	ch, ok := doc.Channels["channel"]
	if !ok {
		t.Fatalf("no fallback channel; channels=%v", doc.Channels)
	}
	if ch.Address != ":::" {
		t.Errorf("fallback channel address = %q, want the raw ':::'", ch.Address)
	}
}

func TestGenerate_MarshalsToValidJSON(t *testing.T) {
	doc := asyncapi.Generate(newDocument(t, nil, event("order:created")))
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

func TestGenerate_HandBuiltSchemaEdges(t *testing.T) {
	src := contract.Document{Requests: []contract.Request{
		{Topic: "a:nobody"}, // no request schema at all
		{Topic: "a:untyped", Request: contract.Schema{"nullable": true, "enum": []any{"x"}}},
	}}
	doc := asyncapi.Generate(src)
	if p := doc.Channels["a_nobody"].Messages["request"].Payload; p != nil {
		t.Errorf("payload = %v, want nil", p)
	}
	if p := doc.Channels["a_untyped"].Messages["request"].Payload; p["nullable"] != nil || p["type"] != nil {
		t.Errorf("payload = %v, want nullable dropped and no type invented", p)
	}
}
