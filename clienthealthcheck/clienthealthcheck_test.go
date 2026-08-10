package clienthealthcheck

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/mesh"
)

// meshResponder answers the reserved benzene:mesh topic (the only topic this check probes) with a
// scripted result, and fails any other topic so a stray call is obvious.
func meshResponder(reply benzene.Result[json.RawMessage]) client.Sender {
	return client.SenderFunc(func(_ context.Context, topic benzene.Topic, _ map[string]string, _ []byte) benzene.Result[json.RawMessage] {
		if topic.ID == mesh.TopicID {
			return reply
		}
		return benzene.NotFound[json.RawMessage]("unexpected topic " + topic.ID)
	})
}

func descriptorReply(t *testing.T, hash string) benzene.Result[json.RawMessage] {
	t.Helper()
	data, err := json.Marshal(mesh.Descriptor{Service: "orders", DescriptorHash: hash})
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	return benzene.Ok(json.RawMessage(data))
}

func TestServiceCheck_Name(t *testing.T) {
	if got := New("orders", meshResponder(descriptorReply(t, "h1"))).Name(); got != "orders" {
		t.Errorf("Name() = %q, want orders", got)
	}
}

func TestServiceCheck_UnreachableIsFailed(t *testing.T) {
	// A transport failure, and an up-but-doesn't-serve-a-descriptor (not-found), both read as
	// unreachable-for-contract-purposes: failed.
	tests := map[string]benzene.Result[json.RawMessage]{
		"transport failure":     benzene.ServiceUnavailable[json.RawMessage]("down"),
		"descriptor not served": benzene.NotFound[json.RawMessage]("no benzene:mesh handler"),
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			result := New("orders", meshResponder(reply)).Check(context.Background())
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
	// A reachable provider - even one whose own health is failing - serves its descriptor with a
	// success status, so this contract check reports ok (it never couples to transient health).
	result := New("orders", meshResponder(descriptorReply(t, "h1"))).Check(context.Background())
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
	result := New("orders", meshResponder(descriptorReply(t, "hash-v1")), WithExpectedContractHash("hash-v1")).
		Check(context.Background())
	if result.Status != healthcheck.StatusOk {
		t.Fatalf("status = %q, want ok", result.Status)
	}
	if result.Data["drifted"] != false || result.Data["contractHash"] != "hash-v1" {
		t.Errorf("data = %+v, want drifted=false and contractHash=hash-v1", result.Data)
	}
}

func TestServiceCheck_ContractDriftIsWarning(t *testing.T) {
	result := New("orders", meshResponder(descriptorReply(t, "hash-v2")), WithExpectedContractHash("hash-v1")).
		Check(context.Background())
	if result.Status != healthcheck.StatusWarning {
		t.Fatalf("status = %q, want warning (drift is degraded, not fatal)", result.Status)
	}
	if result.Data["drifted"] != true || result.Data["contractHash"] != "hash-v2" || result.Data["expectedHash"] != "hash-v1" {
		t.Errorf("data = %+v, want drifted=true with both hashes", result.Data)
	}
}

func TestServiceCheck_DriftUnassessableStaysOk(t *testing.T) {
	// Reachable (the descriptor answered with success), drift-detection configured, but no usable hash
	// came back: ok (reachability passed), with the gap recorded rather than hidden.
	tests := map[string]benzene.Result[json.RawMessage]{
		"descriptor unparseable":         benzene.Ok(json.RawMessage(`not json`)),
		"descriptor without a hash":      benzene.Ok(json.RawMessage(`{"service":"orders"}`)),
		"reachable success with no body": {Status: benzene.StatusOk},
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			result := New("orders", meshResponder(reply), WithExpectedContractHash("hash-v1")).
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
