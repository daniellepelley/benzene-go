package gengo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daniellepelley/benzene-go/codegen/contractdoc"
)

// TestDogfood_CommittedPaymentsCaptureClientIsFresh is this module's half of
// examples/codegen-helloworld's dogfood: it regenerates the payments:capture atomic client from
// the SAME contracts/payments.spec.json that example commits, and diffs the regenerated source
// against what is actually committed at examples/codegen-helloworld/paymentscapture/ - i.e. it
// reproduces the `go generate ./... && git diff --exit-code` CI check (docs/codegen-client.md)
// as an ordinary `go test`, so a change to the generator or the contract document that the
// example's committed output was not regenerated against fails loudly here rather than silently
// drifting. The generated package cannot itself be imported from here (it lives in the
// dependency-free root module; this module must not become that root module's dependency, nor
// vice versa - see docs/codegen-client.md) so this compares generated source text, not Go values.
func TestDogfood_CommittedPaymentsCaptureClientIsFresh(t *testing.T) {
	exampleDir := filepath.Join("..", "..", "examples", "codegen-helloworld")
	specPath := filepath.Join(exampleDir, "contracts", "payments.spec.json")

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("reading %s: %v", specPath, err)
	}
	doc, err := contractdoc.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %s: %v", specPath, err)
	}

	clients, err := GenerateAtomicClients(doc, AtomicOptions{Topics: []string{"payments:capture"}})
	if err != nil {
		t.Fatalf("GenerateAtomicClients: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1", len(clients))
	}

	committedDir := filepath.Join(exampleDir, clients[0].Dir)
	for _, f := range clients[0].Files {
		committedPath := filepath.Join(committedDir, f.Name)
		committed, err := os.ReadFile(committedPath)
		if err != nil {
			t.Fatalf("reading committed %s: %v (run `go generate ./...` in %s and commit the result)", committedPath, err, exampleDir)
		}
		if string(committed) != f.Source {
			t.Errorf("%s is stale - regenerate with `go generate ./...` in %s and commit the result.\n--- committed ---\n%s\n--- freshly generated ---\n%s",
				committedPath, exampleDir, committed, f.Source)
		}
	}
}
