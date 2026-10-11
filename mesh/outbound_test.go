package mesh

import (
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
)

// OutboundRegistry is contract's; this pins only the deprecated alias and constructor.
func TestNewOutboundRegistry_IsContractsRegistry(t *testing.T) {
	var r *contract.OutboundRegistry = NewOutboundRegistry()
	must(t, contract.RegisterEvent[echoRequest](r, benzene.NewTopic("order:placed")))
	if kind, ok := r.Kind(benzene.NewTopic("order:placed")); !ok || kind != contract.KindEvent {
		t.Errorf("Kind() = (%v, %v), want (event, true)", kind, ok)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
