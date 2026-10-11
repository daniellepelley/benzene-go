package gengo

import (
	"os"
	"strings"
	"testing"

	"github.com/daniellepelley/benzene-go/codegen/contractdoc"
)

func loadPaymentsDoc(t *testing.T) *contractdoc.Document {
	t.Helper()
	raw, err := os.ReadFile("testdata/payments.spec.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	doc, err := contractdoc.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestGenerateServiceClient_DomainDefault(t *testing.T) {
	doc := loadPaymentsDoc(t)

	scoped, err := contractdoc.ApplyScope(doc, contractdoc.ScopeOptions{})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}

	files, err := GenerateServiceClient(scoped, ServiceOptions{ServiceName: "Payments", PackageName: "payments"})
	if err != nil {
		t.Fatalf("GenerateServiceClient: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}

	var clientSrc, typesSrc string
	for _, f := range files {
		switch f.Name {
		case "client.go":
			clientSrc = f.Source
		case "types.go":
			typesSrc = f.Source
		default:
			t.Errorf("unexpected file %q", f.Name)
		}
	}

	// A benzene: entry is never a client method (contract-document.md §5.1).
	if strings.Contains(clientSrc, "benzene:") || strings.Contains(typesSrc, "benzene:") {
		t.Error("reserved topic leaked into domain-only generated output")
	}

	for _, want := range []string{
		`"payments:capture"`,
		`"payments:get-all"`,
		"func NewPaymentsClient(sender client.Sender) *PaymentsClient",
		"func (c *PaymentsClient) CapturePayments(ctx context.Context, req CapturePayment) (benzene.Result[PaymentDto], error)",
		"func (c *PaymentsClient) GetallPayments(ctx context.Context, req Void) (benzene.Result[[]PaymentDto], error)",
		"const ContractHash",
		"var RequiredTopics = []benzene.Topic{",
	} {
		if !strings.Contains(clientSrc, want) {
			t.Errorf("client.go missing %q\n---\n%s", want, clientSrc)
		}
	}

	for _, want := range []string{
		"type CapturePayment struct",
		"type PaymentDto struct",
		"type Void struct",
		`json:"OrderId"`,
		`json:"Amount,omitempty"`,
	} {
		if !strings.Contains(typesSrc, want) {
			t.Errorf("types.go missing %q\n---\n%s", want, typesSrc)
		}
	}

	// contract-document.md §5.2: the include-list scopes requests[] ONLY - components is left
	// unnarrowed, so a component reached only from the events/sends (OutboundShipmentBook,
	// OutboundPaymentCaptured) is still generated here. Only a §5.3 topic-scoped (atomic)
	// projection narrows components - see TestGenerateAtomicClients_PaymentsCaptureOnly.
	for _, want := range []string{"type OutboundShipmentBook struct", "type OutboundPaymentCaptured struct"} {
		if !strings.Contains(typesSrc, want) {
			t.Errorf("types.go missing %q (components is unnarrowed for a service-level client)", want)
		}
	}

	// The default client embeds the whole-service contractHash: the value the provider serves.
	served, _ := contractdoc.Hash(doc)
	if !strings.Contains(clientSrc, `const ContractHash = "`+served+`"`) {
		t.Errorf("client.go ContractHash is not the served %s", served)
	}
}

func TestGenerateServiceClient_AbsentResponseIsNoPayload(t *testing.T) {
	doc, err := contractdoc.Parse([]byte(`{"schemaDialect": "openapi-3.0", "service": "s",
		"requests": [{"topic": "orders:void", "request": {"$ref": "#/components/schemas/VoidOrder"}},
			{"topic": "orders:any", "request": {"$ref": "#/components/schemas/VoidOrder"}, "response": {}}],
		"components": {"schemas": {"VoidOrder": {"type": "object", "properties": {"id": {"type": "string"},
			"previous": {"type": "object", "nullable": true, "allOf": [{"$ref": "#/components/schemas/VoidOrder"}]}}, "required": ["id", "previous"]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	files, err := GenerateServiceClient(doc, ServiceOptions{ServiceName: "Orders", PackageName: "orders"})
	if err != nil {
		t.Fatalf("GenerateServiceClient: %v", err)
	}
	src := files[0].Source + files[1].Source
	for _, want := range []string{
		"func (c *OrdersClient) VoidOrders(ctx context.Context, req VoidOrder) (benzene.Result[struct{}], error)",
		"func (c *OrdersClient) AnyOrders(ctx context.Context, req VoidOrder) (benzene.Result[any], error)",
		"Previous *VoidOrder `json:\"previous,omitempty\"`",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}
}

func TestGenerateServiceClient_InvalidPackageName(t *testing.T) {
	doc := loadPaymentsDoc(t)
	scoped, err := contractdoc.ApplyScope(doc, contractdoc.ScopeOptions{})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}
	if _, err := GenerateServiceClient(scoped, ServiceOptions{ServiceName: "Payments", PackageName: "not-valid"}); err == nil {
		t.Fatal("expected an error for an invalid package name")
	}
}

func TestGenerateAtomicClients_PaymentsCaptureOnly(t *testing.T) {
	doc := loadPaymentsDoc(t)

	clients, err := GenerateAtomicClients(doc, AtomicOptions{Topics: []string{"payments:capture"}})
	if err != nil {
		t.Fatalf("GenerateAtomicClients: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("got %d atomic clients, want 1", len(clients))
	}

	c := clients[0]
	if c.Topic != "payments:capture" {
		t.Errorf("Topic = %q", c.Topic)
	}
	if c.Dir != "paymentscapture" {
		t.Errorf("Dir = %q, want paymentscapture", c.Dir)
	}

	var clientSrc, typesSrc string
	for _, f := range c.Files {
		switch f.Name {
		case "client.go":
			clientSrc = f.Source
		case "types.go":
			typesSrc = f.Source
		}
	}

	if strings.Contains(clientSrc, "benzene:") || strings.Contains(typesSrc, "benzene:") {
		t.Error("reserved topic leaked into topic-scoped generated output")
	}
	if !strings.Contains(clientSrc, `"payments:capture"`) {
		t.Error("client.go missing the payments:capture topic literal")
	}
	if strings.Contains(clientSrc, "payments:get-all") {
		t.Error("atomic client for payments:capture must not reference payments:get-all")
	}
	// The closure from CapturePayment/PaymentDto does not reach Void or the outbound types.
	for _, notWant := range []string{"type Void struct", "OutboundShipmentBook", "OutboundPaymentCaptured"} {
		if strings.Contains(typesSrc, notWant) {
			t.Errorf("types.go unexpectedly contains %q (outside this topic's schema closure)", notWant)
		}
	}
	for _, want := range []string{"type CapturePayment struct", "type PaymentDto struct"} {
		if !strings.Contains(typesSrc, want) {
			t.Errorf("types.go missing %q", want)
		}
	}
}

func TestGenerateAtomicClients_UnknownTopicFailsLoud(t *testing.T) {
	doc := loadPaymentsDoc(t)
	_, err := GenerateAtomicClients(doc, AtomicOptions{Topics: []string{"not-a-topic"}})
	if err == nil {
		t.Fatal("expected an error for an unknown topic")
	}
}
