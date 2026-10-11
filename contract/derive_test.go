package contract

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func handler[TReq, TRes any](_ context.Context, _ TReq) benzene.Result[TRes] {
	var zero TRes
	return benzene.Ok(zero)
}

type Item struct {
	SKU string `json:"sku"`
}

type Basket struct {
	Items    []Item `json:"items"`
	Previous *Item  `json:"previous"`
}

// sameItem is another Go type that claims the name Item with the same shape: the two may share one
// component.
type sameItem struct {
	SKU string `json:"sku"`
}

func (sameItem) BenzeneSchemaName() string { return "Item" }

// otherItem claims Item with a different shape: a collision.
type otherItem struct {
	PaymentID string `json:"paymentId"`
}

func (otherItem) BenzeneSchemaName() string { return "Item" }

type Wrapper struct {
	Inner otherItem `json:"inner"`
}

type Named struct{}

// Pair reaches Named twice and no colliding type; Holder reaches otherItem only through a nullable
// wrapper's allOf.
type Pair struct {
	A Named `json:"a"`
	B Named `json:"b"`
}

type Holder struct {
	P *otherItem `json:"p"`
}

func TestDerive_Document(t *testing.T) {
	registry := benzene.NewRegistry()
	must(t, benzene.Register(registry, benzene.NewTopic("orders:create").WithVersion("v2"), handler[Basket, Item]))
	must(t, benzene.Register(registry, benzene.NewTopic("orders:create").WithVersion("v1"), handler[Basket, any]))
	must(t, benzene.Register(registry, benzene.NewTopic("orders:void"), handler[Item, struct{}]))
	must(t, benzene.Register(registry, benzene.NewTopic("orders:ping"), handler[struct{}, Named]))
	must(t, benzene.Register(registry, benzene.NewTopic("benzene:custom"), handler[Item, Item]))

	outbound := NewOutboundRegistry()
	must(t, RegisterEvent[sameItem](outbound, benzene.NewTopic("orders:created")))
	must(t, RegisterEvent[Item](outbound, benzene.NewTopic("benzene:internal")))
	must(t, RegisterSend[Item](outbound, benzene.NewTopic("payments:take")))
	must(t, RegisterSend[Item](outbound, benzene.NewTopic("stock:reserve"), WithResponse[any]()))
	must(t, RegisterSend[Item](outbound, benzene.NewTopic("benzene:other")))

	doc, err := Derive(registry, outbound, Info{
		Service: "orders", ServiceVersion: "1.2.0", VersionScheme: "semver", Description: "Order intake",
		MessageEndpoint: "/benzene/invoke", Transports: []string{"http"},
		HTTPRoutes: []HTTPRoute{
			{Method: "post", Path: "/orders", Topic: benzene.NewTopic("orders:create")},
			{Method: "PUT", Path: "/v1/orders", Topic: benzene.NewTopic("orders:create").WithVersion("v1")},
			{Method: "GET", Path: "/health", Topic: benzene.NewTopic("benzene:healthcheck")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(doc)
	var got map[string]any
	must(t, json.Unmarshal(raw, &got))
	for key, want := range map[string]any{
		"schemaDialect": "openapi-3.0", "service": "orders", "serviceVersion": "1.2.0",
		"versionScheme": "semver", "description": "Order intake", "messageEndpoint": "/benzene/invoke",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}

	requests := got["requests"].([]any)
	if len(requests) != 4 {
		t.Fatalf("requests = %v: benzene: topics are never listed", requests)
	}
	byTopic := map[string]map[string]any{}
	for _, r := range requests {
		entry := r.(map[string]any)
		key := entry["topic"].(string)
		if v, ok := entry["version"]; ok {
			key += "@" + v.(string)
		}
		byTopic[key] = entry
	}
	if m := canon(t, byTopic["orders:create@v1"]["httpMappings"]); m != `[{"method":"POST","path":"/orders"},{"method":"PUT","path":"/v1/orders"}]` {
		t.Errorf("v1 httpMappings = %s", m)
	}
	if m := canon(t, byTopic["orders:create@v2"]["httpMappings"]); m != `[{"method":"POST","path":"/orders"}]` {
		t.Errorf("v2 httpMappings = %s", m)
	}
	if _, ok := byTopic["orders:void"]["response"]; ok {
		t.Error("a struct{} response must be absent")
	}
	if r := canon(t, byTopic["orders:create@v1"]["response"]); r != `{}` {
		t.Errorf("an any response = %s, want {}", r)
	}
	if r := canon(t, byTopic["orders:ping"]["response"]); r != `{"$ref":"#/components/schemas/Named"}` {
		t.Errorf("a named empty struct response = %s, want its component", r)
	}
	if _, ok := byTopic["orders:create@v1"]["httpMappings"]; !ok {
		t.Error("httpMappings missing")
	}
	if _, ok := byTopic["orders:void"]["httpMappings"]; ok {
		t.Error("httpMappings must be omitted when empty")
	}

	if events := canon(t, got["events"]); events != `[{"message":{"$ref":"#/components/schemas/Item"},"topic":"orders:created"}]` {
		t.Errorf("events = %s (sameItem shares Item's component)", events)
	}
	if sends := canon(t, got["sends"]); sends != `[{"request":{"$ref":"#/components/schemas/Item"},"topic":"payments:take"},`+
		`{"request":{"$ref":"#/components/schemas/Item"},"response":{},"topic":"stock:reserve"}]` {
		t.Errorf("sends = %s", sends)
	}
	if names := keysOf(doc.Components.Schemas); strings.Join(names, ",") != "Basket,Item,Named" {
		t.Errorf("components = %v", names)
	}

	contractHash, registrationHash, err := doc.Hashes()
	if err != nil || contractHash != doc.ContractHash || registrationHash != doc.RegistrationHash {
		t.Errorf("document hashes (%s, %s) do not match its entries (%s, %s, %v)", doc.ContractHash, doc.RegistrationHash, contractHash, registrationHash, err)
	}
	if !strings.HasPrefix(doc.ContractHash, HashPrefix) || doc.ContractHash == doc.RegistrationHash {
		t.Errorf("hashes = %s, %s", doc.ContractHash, doc.RegistrationHash)
	}
}

func keysOf(m map[string]Schema) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestDerive_EmptyDocumentHasEmptyLists(t *testing.T) {
	doc, err := Derive(benzene.NewRegistry(), nil, Info{Service: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	for _, want := range []string{`"requests":[]`, `"events":[]`, `"sends":[]`, `"components":{"schemas":{}}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s lacks %s", raw, want)
		}
	}
	for _, absent := range []string{"serviceVersion", "transports", "messageEndpoint", "description", "placement", "profile"} {
		if strings.Contains(string(raw), absent) {
			t.Errorf("%s carries an empty %s", raw, absent)
		}
	}
	if !strings.Contains(string(raw), `"runtime":"go"`) {
		t.Errorf("%s lacks the runtime", raw)
	}
}

func TestDerive_DegradedNamesTheMembersANilOutboundLeavesUndetermined(t *testing.T) {
	doc, err := Derive(benzene.NewRegistry(), nil, Info{Service: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(doc.Degraded, ",") != "events,sends" {
		t.Errorf("Degraded = %v, want events and sends", doc.Degraded)
	}

	declared, err := Derive(benzene.NewRegistry(), NewOutboundRegistry(), Info{Service: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if declared.Degraded != nil {
		t.Errorf("an empty OutboundRegistry asserts none, yet Degraded = %v", declared.Degraded)
	}
	raw, _ := json.Marshal(declared)
	if strings.Contains(string(raw), "degraded") {
		t.Errorf("%s carries an empty degraded", raw)
	}
	// Degraded, like every deployment-side field, is outside both hashes.
	if doc.ContractHash != declared.ContractHash || doc.RegistrationHash != declared.RegistrationHash {
		t.Error("Degraded changed a hash")
	}
}

func TestDerive_DeploymentFieldsAreCarriedAndOutsideBothHashes(t *testing.T) {
	registry := benzene.NewRegistry()
	must(t, benzene.Register(registry, benzene.NewTopic("orders:item"), handler[Item, struct{}]))
	bare, err := Derive(registry, NewOutboundRegistry(), Info{Service: "s"})
	if err != nil {
		t.Fatal(err)
	}
	placed, err := Derive(registry, NewOutboundRegistry(), Info{
		Service:   "s",
		Placement: &Placement{Cloud: "aws", Region: "eu-west-1"},
		Profile:   &Profile{Name: "cloud-service", Missing: []string{"R3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(placed)
	for _, want := range []string{`"placement":{"cloud":"aws","region":"eu-west-1"}`, `"profile":{"name":"cloud-service","missing":["R3"]}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s lacks %s", raw, want)
		}
	}
	if bare.ContractHash != placed.ContractHash || bare.RegistrationHash != placed.RegistrationHash {
		t.Error("placement or profile changed a hash")
	}
}

func TestDerive_CollisionFailsNamingTypesTopicsAndRemedy(t *testing.T) {
	registry := benzene.NewRegistry()
	must(t, benzene.Register(registry, benzene.NewTopic("orders:item"), handler[Item, struct{}]))
	must(t, benzene.Register(registry, benzene.NewTopic("payments:wrap"), handler[Wrapper, struct{}]))
	outbound := NewOutboundRegistry()
	must(t, RegisterSend[Basket](outbound, benzene.NewTopic("basket:send")))
	must(t, RegisterEvent[Wrapper](outbound, benzene.NewTopic("wrap:event")))
	must(t, RegisterEvent[Pair](outbound, benzene.NewTopic("pair:event")))
	must(t, RegisterEvent[Holder](outbound, benzene.NewTopic("holder:event")))

	_, err := Derive(registry, outbound, Info{Service: "s"})
	var collision *NameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("error = %v, want a *NameCollisionError", err)
	}
	if collision.Name != "Item" {
		t.Errorf("Name = %q", collision.Name)
	}
	if strings.Join(collision.Types, ",") != "github.com/daniellepelley/benzene-go/contract.Item,github.com/daniellepelley/benzene-go/contract.otherItem" {
		t.Errorf("Types = %v", collision.Types)
	}
	if strings.Join(collision.Topics, ",") != "basket:send,holder:event,orders:item,payments:wrap,wrap:event" {
		t.Errorf("Topics = %v", collision.Topics)
	}
	if msg := err.Error(); !strings.Contains(msg, "BenzeneSchemaName") || !strings.Contains(msg, `"Item"`) {
		t.Errorf("message %q lacks the name or the remedy", msg)
	}
}

func TestDerive_RequiresServiceAndRegistry(t *testing.T) {
	if _, err := Derive(benzene.NewRegistry(), nil, Info{}); err == nil {
		t.Error("expected an error without a service name")
	}
	if _, err := Derive(nil, nil, Info{Service: "s"}); err == nil {
		t.Error("expected an error without a registry")
	}
}

func TestDerive_UnhashableDocumentFails(t *testing.T) {
	// A NaN cannot be written as JSON, so neither marshaling nor hashing can succeed.
	if _, _, err := (Document{Requests: []Request{{Example: nanValue()}}}).Hashes(); err == nil {
		t.Error("expected an error")
	}
}
