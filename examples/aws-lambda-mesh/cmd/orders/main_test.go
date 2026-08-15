package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"

	"github.com/daniellepelley/benzene-go/examples/aws-lambda-mesh/domain"
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

func TestOrders_PostOrders_FansOutToPaymentsAndOrderPlaced(t *testing.T) {
	payments := &recordingSender{}
	orderPlaced := &recordingSender{}
	app := newApp(payments, orderPlaced, nil)

	raw, err := app.Handler()(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodPost, "/orders",
		domain.CreateOrderRequest{CustomerID: "cust-1", SKU: "espresso", Quantity: 2}, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		StatusCode int    `json:"statusCode"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("statusCode = %d, want 201; body = %s", resp.StatusCode, resp.Body)
	}

	if len(payments.calls) != 1 || payments.calls[0].topic != benzene.NewTopic(domain.TopicPaymentsCapture) {
		t.Errorf("payments.calls = %+v, want one send to %s", payments.calls, domain.TopicPaymentsCapture)
	}
	if len(orderPlaced.calls) != 1 || orderPlaced.calls[0].topic != benzene.NewTopic(domain.TopicOrderPlaced) {
		t.Errorf("orderPlaced.calls = %+v, want one send to %s", orderPlaced.calls, domain.TopicOrderPlaced)
	}
}

func TestOrders_HealthCheck(t *testing.T) {
	app := newApp(nil, nil, nil)
	raw, err := app.Handler()(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodGet, "/benzene/health", nil, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct{ StatusCode int }
	_ = json.Unmarshal(raw, &resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /benzene/health statusCode = %d, want 200", resp.StatusCode)
	}
}
