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

// invokeQueue drives the Service Bus queue-trigger local path with a message carrying its topic
// as a UserProperties application property, matching how azureservicebus.Client actually
// publishes (see azurefunctions.QueueHandler's own doc comment for the resolution order).
func invokeQueue(t *testing.T, handler http.Handler, localPath, topic, body string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"Data":     map[string]any{"mySbMsg": body},
		"Metadata": map[string]any{"UserProperties": map[string]any{"topic": topic}},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, localPath, strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestPayments_PaymentTake_ChainsToShippingAndPaymentCaptured(t *testing.T) {
	shipping := &recordingSender{}
	paymentCaptured := &recordingSender{}
	app := newApp(shipping, paymentCaptured, nil)

	body, err := json.Marshal(domain.TakePaymentRequest{OrderID: "order-1", Amount: 20})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	rec := invokeQueue(t, mux(app), "/PaymentTake", domain.TopicPaymentTake, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	if len(shipping.calls) != 1 || shipping.calls[0].topic != benzene.NewTopic(domain.TopicShipmentBook) {
		t.Errorf("shipping.calls = %+v, want one send to %s", shipping.calls, domain.TopicShipmentBook)
	}
	if len(paymentCaptured.calls) != 1 || paymentCaptured.calls[0].topic != benzene.NewTopic(domain.TopicPaymentCaptured) {
		t.Errorf("paymentCaptured.calls = %+v, want one send to %s", paymentCaptured.calls, domain.TopicPaymentCaptured)
	}
}

func TestPayments_HealthCheck(t *testing.T) {
	app := newApp(nil, nil, nil)
	res := invokeHTTP(t, mux(app), "/Health", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Errorf("GET /Health statusCode = %s, want 200", res.StatusCode)
	}
}

func TestPayments_Spec(t *testing.T) {
	app := newApp(nil, nil, nil)
	res := invokeHTTP(t, mux(app), "/Spec", http.MethodGet, "")
	if res.StatusCode != "200" {
		t.Fatalf("GET /Spec statusCode = %s, want 200", res.StatusCode)
	}
	if !strings.Contains(res.Body, `"service":"payments"`) {
		t.Errorf("spec body = %s, want it to describe the payments service", res.Body)
	}
}

// TestPayments_DescriptorDeclaresWhatItSends proves this Function actually wires the outbound half of
// its contract: what domain.RegisterOutbound declares for payments must reach the descriptor it
// announces, since Descriptor.Consumes - not observed traffic - is what draws this service's
// consumer edges on the mesh's topic catalog (mesh.md §4). Built with nil senders on purpose: the
// declaration is a contract, not a function of which transports a given deployment happened to
// wire up.
func TestPayments_DescriptorDeclaresWhatItSends(t *testing.T) {
	desc := newApp(nil, nil, nil).Descriptor()

	got := []string{}
	for _, topic := range desc.Consumes {
		got = append(got, topic.ID)
	}
	// Sorted by topic ID, matching mesh.OutboundRegistry.Topics().
	if want := []string{domain.TopicPaymentCaptured, domain.TopicShipmentBook}; !slices.Equal(got, want) {
		t.Errorf("Descriptor.Consumes = %v, want %v", got, want)
	}
	if len(desc.Degraded) != 0 {
		t.Errorf("Descriptor.Degraded = %v, want none (both feeds are wired)", desc.Degraded)
	}
}
