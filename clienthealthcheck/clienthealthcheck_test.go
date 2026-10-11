package clienthealthcheck

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/wire"
)

// specResponder answers the reserved benzene:spec topic (the only topic this check probes) with a
// scripted result, and fails any other topic so a stray call is obvious.
func specResponder(reply benzene.Result[json.RawMessage]) client.Sender {
	return client.SenderFunc(func(_ context.Context, topic benzene.Topic, _ map[string]string, _ []byte) benzene.Result[json.RawMessage] {
		if topic.ID == contract.TopicID {
			return reply
		}
		return benzene.NotFound[json.RawMessage]("unexpected topic " + topic.ID)
	})
}

func documentReply(t *testing.T, hash string) benzene.Result[json.RawMessage] {
	t.Helper()
	data, err := json.Marshal(contract.Document{SchemaDialect: contract.SchemaDialect, Service: "orders", ContractHash: hash})
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return benzene.Ok(json.RawMessage(data))
}

// TestServiceCheck_AgainstARealProvider reads the hash a provider's contract.Middleware serves.
func TestServiceCheck_AgainstARealProvider(t *testing.T) {
	registry := benzene.NewRegistry()
	doc, err := contract.Derive(registry, nil, contract.Info{Service: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := benzene.NewPipeline(contract.Middleware(doc), benzene.RouterMiddleware(registry))
	sender := client.SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, body []byte) benzene.Result[json.RawMessage] {
		return envelope.ToResult(envelope.Dispatch(ctx, pipeline, benzene.NewContainer(), wire.Request{Topic: topic.ID, Headers: headers, Body: string(body)}))
	})

	if result := New("orders", sender, WithExpectedContractHash(doc.ContractHash)).Check(context.Background()); result.Status != healthcheck.StatusOk || result.Data["drifted"] != false {
		t.Errorf("matching hash: %+v", result)
	}
	if result := New("orders", sender, WithExpectedContractHash("sha256:stale")).Check(context.Background()); result.Status != healthcheck.StatusWarning {
		t.Errorf("stale hash: %+v", result)
	}
}

func TestServiceCheck_Name(t *testing.T) {
	if got := New("orders", specResponder(documentReply(t, "h1"))).Name(); got != "orders" {
		t.Errorf("Name() = %q, want orders", got)
	}
}

func TestServiceCheck_UnreachableIsFailed(t *testing.T) {
	// A transport failure, and an up-but-doesn't-serve-a-descriptor (not-found), both read as
	// unreachable-for-contract-purposes: failed.
	tests := map[string]benzene.Result[json.RawMessage]{
		"transport failure":   benzene.ServiceUnavailable[json.RawMessage]("down"),
		"contract not served": benzene.NotFound[json.RawMessage]("no benzene:spec handler"),
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			result := New("orders", specResponder(reply)).Check(context.Background())
			if result.Status != healthcheck.StatusFailed {
				t.Fatalf("status = %q, want failed", result.Status)
			}
			if result.Type != "orders" || result.Data["reachable"] != false {
				t.Errorf("result = %+v, want type orders and reachable=false", result)
			}
		})
	}
}

func TestServiceCheck_ReachableWithoutDriftDetectionIsOk(t *testing.T) {
	// A reachable provider - even one whose own health is failing - serves its contract with a
	// success status, so this contract check reports ok (it never couples to transient health).
	result := New("orders", specResponder(documentReply(t, "h1"))).Check(context.Background())
	if result.Status != healthcheck.StatusOk {
		t.Fatalf("status = %q, want ok", result.Status)
	}
	if result.Data["reachable"] != true {
		t.Errorf("data = %+v, want reachable=true", result.Data)
	}
	if _, hasHash := result.Data["contractHash"]; hasHash {
		t.Error("reachability-only check should not report a contract hash")
	}
}

func TestServiceCheck_ContractMatchIsOk(t *testing.T) {
	result := New("orders", specResponder(documentReply(t, "hash-v1")), WithExpectedContractHash("hash-v1")).
		Check(context.Background())
	if result.Status != healthcheck.StatusOk {
		t.Fatalf("status = %q, want ok", result.Status)
	}
	if result.Data["drifted"] != false || result.Data["contractHash"] != "hash-v1" {
		t.Errorf("data = %+v, want drifted=false and contractHash=hash-v1", result.Data)
	}
}

func TestServiceCheck_ContractDriftIsWarning(t *testing.T) {
	result := New("orders", specResponder(documentReply(t, "hash-v2")), WithExpectedContractHash("hash-v1")).
		Check(context.Background())
	if result.Status != healthcheck.StatusWarning {
		t.Fatalf("status = %q, want warning (drift is degraded, not fatal)", result.Status)
	}
	if result.Data["drifted"] != true || result.Data["contractHash"] != "hash-v2" || result.Data["expectedHash"] != "hash-v1" {
		t.Errorf("data = %+v, want drifted=true with both hashes", result.Data)
	}
}

func TestServiceCheck_DriftUnassessableStaysOk(t *testing.T) {
	// Reachable (benzene:spec answered with success), drift-detection configured, but no usable hash
	// came back: ok (reachability passed), with the gap recorded rather than hidden.
	tests := map[string]benzene.Result[json.RawMessage]{
		"document unparseable":           benzene.Ok(json.RawMessage(`not json`)),
		"document without a hash":        benzene.Ok(json.RawMessage(`{"service":"orders"}`)),
		"reachable success with no body": {Status: benzene.StatusOk},
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			result := New("orders", specResponder(reply), WithExpectedContractHash("hash-v1")).
				Check(context.Background())
			if result.Status != healthcheck.StatusOk {
				t.Fatalf("status = %q, want ok", result.Status)
			}
			if result.Data["driftAssessed"] != false {
				t.Errorf("data = %+v, want driftAssessed=false", result.Data)
			}
		})
	}
}
