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

// responder answers by reserved topic id, so a test scripts the downstream's healthcheck and mesh
// descriptor replies independently.
type responder func(topicID string) benzene.Result[json.RawMessage]

func sender(r responder) client.Sender {
	return client.SenderFunc(func(_ context.Context, topic benzene.Topic, _ map[string]string, _ []byte) benzene.Result[json.RawMessage] {
		return r(topic.ID)
	})
}

func healthOK(topicID string) benzene.Result[json.RawMessage] {
	if topicID == healthcheck.ReservedTopic {
		return benzene.Ok(json.RawMessage(`{"isHealthy":true}`))
	}
	return benzene.NotFound[json.RawMessage]("no descriptor")
}

func descriptorJSON(t *testing.T, hash string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(mesh.Descriptor{Service: "orders", DescriptorHash: hash})
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	return data
}

func TestServiceCheck_Name(t *testing.T) {
	if got := New("orders", sender(healthOK)).Name(); got != "orders" {
		t.Errorf("Name() = %q, want orders", got)
	}
}

func TestServiceCheck_UnreachableIsFailed(t *testing.T) {
	check := New("orders", sender(func(string) benzene.Result[json.RawMessage] {
		return benzene.ServiceUnavailable[json.RawMessage]("down")
	}))
	result := check.Check(context.Background())
	if result.Status != healthcheck.StatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if result.Type != "orders" || result.Data["reachable"] != false {
		t.Errorf("result = %+v, want type orders and reachable=false", result)
	}
}

func TestServiceCheck_ReachableWithoutDriftDetectionIsOk(t *testing.T) {
	result := New("orders", sender(healthOK)).Check(context.Background())
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
	check := New("orders", sender(func(topicID string) benzene.Result[json.RawMessage] {
		if topicID == mesh.TopicID {
			return benzene.Ok(descriptorJSON(t, "hash-v1"))
		}
		return healthOK(topicID)
	}), WithExpectedContractHash("hash-v1"))

	result := check.Check(context.Background())
	if result.Status != healthcheck.StatusOk {
		t.Fatalf("status = %q, want ok", result.Status)
	}
	if result.Data["drifted"] != false || result.Data["contractHash"] != "hash-v1" {
		t.Errorf("data = %+v, want drifted=false and contractHash=hash-v1", result.Data)
	}
}

func TestServiceCheck_ContractDriftIsWarning(t *testing.T) {
	check := New("orders", sender(func(topicID string) benzene.Result[json.RawMessage] {
		if topicID == mesh.TopicID {
			return benzene.Ok(descriptorJSON(t, "hash-v2")) // provider moved on
		}
		return healthOK(topicID)
	}), WithExpectedContractHash("hash-v1"))

	result := check.Check(context.Background())
	if result.Status != healthcheck.StatusWarning {
		t.Fatalf("status = %q, want warning (drift is degraded, not fatal)", result.Status)
	}
	if result.Data["drifted"] != true || result.Data["contractHash"] != "hash-v2" || result.Data["expectedHash"] != "hash-v1" {
		t.Errorf("data = %+v, want drifted=true with both hashes", result.Data)
	}
}

func TestServiceCheck_DriftUnassessableStaysOk(t *testing.T) {
	// Reachable, drift-detection configured, but the provider's descriptor can't be read: the profile
	// allows a service to omit the descriptor, so this is ok (reachability passed), not a failure -
	// and the gap is recorded rather than hidden.
	tests := []struct {
		name string
		mesh benzene.Result[json.RawMessage]
	}{
		{"descriptor unreachable", benzene.ServiceUnavailable[json.RawMessage]("no mesh feed")},
		{"descriptor unparseable", benzene.Ok(json.RawMessage(`not json`))},
		{"descriptor without a hash", benzene.Ok(json.RawMessage(`{"service":"orders"}`))},
		{"reachable success with no body", benzene.Result[json.RawMessage]{Status: benzene.StatusOk}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := New("orders", sender(func(topicID string) benzene.Result[json.RawMessage] {
				if topicID == mesh.TopicID {
					return tc.mesh
				}
				return healthOK(topicID)
			}), WithExpectedContractHash("hash-v1"))

			result := check.Check(context.Background())
			if result.Status != healthcheck.StatusOk {
				t.Fatalf("status = %q, want ok", result.Status)
			}
			if result.Data["driftAssessed"] != false {
				t.Errorf("data = %+v, want driftAssessed=false", result.Data)
			}
		})
	}
}
