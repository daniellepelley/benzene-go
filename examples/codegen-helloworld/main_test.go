package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/examples/codegen-helloworld/paymentscapture"
)

// TestGeneratedClient_SendsTypedPayloadToTheRightTopic wires the generated PaymentsCaptureClient
// to a fake client.Sender (a client.SenderFunc closure - no new fake type needed, per this repo's
// existing Sender/SenderFunc seam) and asserts the generated method actually calls
// benzene.Topic{ID: "payments:capture"} with the request marshaled to the wire shape the contract
// document's CapturePayment schema describes, and unmarshals the response back into the generated
// PaymentDto type.
func TestGeneratedClient_SendsTypedPayloadToTheRightTopic(t *testing.T) {
	var gotTopic benzene.Topic
	var gotBody []byte

	sender := client.SenderFunc(func(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
		gotTopic = topic
		gotBody = message
		payload := json.RawMessage(`{"Id":"pay_1","OrderId":"order_1","Amount":42.42,"Currency":"GBP","Status":"captured"}`)
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk, Payload: &payload}
	})

	c := paymentscapture.NewPaymentsCaptureClient(sender)

	amount := 42.42
	req := paymentscapture.CapturePayment{OrderId: "order_1", Amount: &amount, Currency: "GBP"}

	result, err := c.CapturePayments(context.Background(), req)
	if err != nil {
		t.Fatalf("CapturePayments: %v", err)
	}
	if !result.IsSuccessful() {
		t.Fatalf("result not successful: %+v", result)
	}

	if gotTopic != (benzene.Topic{ID: "payments:capture"}) {
		t.Errorf("sent topic = %+v, want payments:capture", gotTopic)
	}

	var sentPayload map[string]any
	if err := json.Unmarshal(gotBody, &sentPayload); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if sentPayload["OrderId"] != "order_1" || sentPayload["Currency"] != "GBP" {
		t.Errorf("sent payload = %v", sentPayload)
	}

	if result.Payload == nil {
		t.Fatal("result payload is nil")
	}
	if result.Payload.Id == nil || *result.Payload.Id != "pay_1" {
		t.Errorf("response Id = %v, want pay_1", result.Payload.Id)
	}
	if result.Payload.Status == nil || *result.Payload.Status != "captured" {
		t.Errorf("response Status = %v, want captured", result.Payload.Status)
	}
}

// expectedPaymentsCaptureContractHash is the contract-document.md §6.2 topic-scoped contractHash
// of contracts/payments.spec.json's payments:capture request - independently recomputed (and
// re-verified byte-for-byte after every `go generate`, see
// codegen/gengo/dogfood_committed_test.go, which regenerates this same client from this same
// source document and diffs it against what's committed here) by the codegen module's own
// contractdoc.Hash, matching contract-hash-cases.json's algorithm exactly. This module (the
// dependency-free root module) cannot import the codegen module to recompute it directly - see
// docs/codegen-client.md - so it is pinned here as a golden value instead.
const expectedPaymentsCaptureContractHash = "sha256:1520b2c06e9f4095f5cd993cd2a4f5ffe3b71dc0ce64f60d5e895cb269934584"

// TestGeneratedClient_ContractHash asserts the committed client's embedded contractHash matches
// the expected value for the payments:capture topic-scoped projection (contract-document.md §5.3,
// §6) - the drift-detection property the embedded hash exists for.
func TestGeneratedClient_ContractHash(t *testing.T) {
	if paymentscapture.ContractHash != expectedPaymentsCaptureContractHash {
		t.Errorf("ContractHash = %s, want %s", paymentscapture.ContractHash, expectedPaymentsCaptureContractHash)
	}
	if !strings.HasPrefix(paymentscapture.ContractHash, "sha256:") {
		t.Errorf("ContractHash = %q, want a sha256: prefix", paymentscapture.ContractHash)
	}
}

// TestGeneratedClient_NoReservedTopicLeaksIn asserts the generated client's requests never
// mention a benzene:* reserved topic (payments.spec.json's benzene:spec entry must not appear),
// per contract-document.md §5.1's domain-only default this generator applies.
func TestGeneratedClient_NoReservedTopicLeaksIn(t *testing.T) {
	for _, topic := range paymentscapture.RequiredTopics {
		if strings.HasPrefix(topic.ID, "benzene:") {
			t.Errorf("RequiredTopics contains a reserved topic: %v", topic)
		}
	}
	if len(paymentscapture.RequiredTopics) != 1 || paymentscapture.RequiredTopics[0].ID != "payments:capture" {
		t.Errorf("RequiredTopics = %v, want exactly [payments:capture]", paymentscapture.RequiredTopics)
	}

	generated, err := os.ReadFile("paymentscapture/client.go")
	if err != nil {
		t.Fatalf("reading generated client.go: %v", err)
	}
	if strings.Contains(string(generated), "benzene:") {
		t.Error("generated client.go must not reference any benzene:* topic")
	}
	generatedTypes, err := os.ReadFile("paymentscapture/types.go")
	if err != nil {
		t.Fatalf("reading generated types.go: %v", err)
	}
	if strings.Contains(string(generatedTypes), "benzene:") {
		t.Error("generated types.go must not reference any benzene:* topic")
	}
}
