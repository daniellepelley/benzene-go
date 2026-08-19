package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"

	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/domain"
)

// recordingSender records every Send call - the same minimal fake domain's own tests use, kept
// local here so this package doesn't need to depend on domain's internal test helpers.
type recordingSender struct {
	calls []struct {
		topic   benzene.Topic
		message []byte
	}
}

func (r *recordingSender) Send(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
	r.calls = append(r.calls, struct {
		topic   benzene.Topic
		message []byte
	}{topic, message})
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

// azureHTTPOutput is the "res" entry of a custom-handler invocation response - the framework-
// mapped outcome of a Route-table dispatch through app.HTTPHandler().
type azureHTTPOutput struct {
	StatusCode string `json:"statusCode"`
	Body       string `json:"body"`
}

func invokeHTTP(t *testing.T, handler http.Handler, localPath, method, body string) azureHTTPOutput {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"Data":     map[string]any{"req": map[string]any{"Method": method, "Headers": map[string]string{}, "Body": body}},
		"Metadata": map[string]any{},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, localPath, strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var inv struct {
		Outputs map[string]azureHTTPOutput `json:"Outputs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rec.Body.String())
	}
	res, ok := inv.Outputs["res"]
	if !ok {
		t.Fatalf("Outputs missing \"res\"; body = %s", rec.Body.String())
	}
	return res
}

func TestOrders_PostOrders_FansOutToPaymentsAndOrderPlaced(t *testing.T) {
	payments := &recordingSender{}
	orderPlaced := &recordingSender{}
	app := newApp(payments, orderPlaced, nil)

	body, err := json.Marshal(domain.CreateOrderRequest{CustomerID: "cust-1", SKU: "espresso", Quantity: 2})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	res := invokeHTTP(t, mux(app), "/Orders", http.MethodPost, string(body))
	if res.StatusCode != "201" {
		t.Fatalf("statusCode = %s, want 201; body = %s", res.StatusCode, res.Body)
	}

	if len(payments.calls) != 1 || payments.calls[0].topic != benzene.NewTopic(domain.TopicPaymentTake) {
		t.Errorf("payments.calls = %+v, want one send to %s", payments.calls, domain.TopicPaymentTake)
	}
	if len(orderPlaced.calls) != 1 || orderPlaced.calls[0].topic != benzene.NewTopic(domain.TopicOrderPlaced) {
		t.Errorf("orderPlaced.calls = %+v, want one send to %s", orderPlaced.calls, domain.TopicOrderPlaced)
	}
}

func TestOrders_HealthCheck(t *testing.T) {
	app := newApp(nil, nil, nil)
	res := invokeHTTP(t, mux(app), "/Health", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Errorf("GET /Health statusCode = %s, want 200", res.StatusCode)
	}
}

func TestOrders_Spec(t *testing.T) {
	app := newApp(nil, nil, nil)
	res := invokeHTTP(t, mux(app), "/Spec", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Fatalf("GET /Spec statusCode = %s, want 200", res.StatusCode)
	}
	if !strings.Contains(res.Body, `"service":"orders"`) {
		t.Errorf("spec body = %s, want it to describe the orders service", res.Body)
	}
}

func TestOrders_NilSendersStillAnswer(t *testing.T) {
	app := newApp(nil, nil, nil)

	body, err := json.Marshal(domain.CreateOrderRequest{CustomerID: "cust-1", SKU: "sku", Quantity: 1})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	res := invokeHTTP(t, mux(app), "/Orders", http.MethodPost, string(body))
	if res.StatusCode != "201" {
		t.Fatalf("statusCode = %s, want 201 even with no downstream wired", res.StatusCode)
	}
}

// TestOrders_DescriptorDeclaresWhatItSends proves this Function actually wires the outbound half of
// its contract: what domain.RegisterOutbound declares for orders must reach the descriptor it
// announces, since Descriptor.Produces - not observed traffic - is what draws this service's
// consumer edges on the mesh's topic catalog (mesh.md §4). Built with nil senders on purpose: the
// declaration is a contract, not a function of which transports a given deployment happened to
// wire up.
func TestOrders_DescriptorDeclaresWhatItSends(t *testing.T) {
	desc := newApp(nil, nil, nil).Descriptor()

	got := []string{}
	for _, topic := range desc.Produces {
		got = append(got, topic.ID)
	}
	// Sorted by topic ID, matching mesh.OutboundRegistry.Topics().
	if want := []string{domain.TopicOrderPlaced, domain.TopicPaymentTake}; !slices.Equal(got, want) {
		t.Errorf("Descriptor.Produces = %v, want %v", got, want)
	}
	if len(desc.Degraded) != 0 {
		t.Errorf("Descriptor.Degraded = %v, want none (both feeds are wired)", desc.Degraded)
	}
}
