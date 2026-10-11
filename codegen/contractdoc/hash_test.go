package contractdoc

import (
	"math"
	"strings"
	"testing"

	"github.com/daniellepelley/benzene-go/contract"
)

func TestHashes_AreTheRuntimeHashes(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	contractHash, registrationHash, err := Hashes(doc)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := doc.MarshalJSON()
	wantContract, wantRegistration, _ := contract.HashDocument(raw)
	if contractHash != wantContract || registrationHash != wantRegistration {
		t.Errorf("Hashes() = (%s, %s), contract.HashDocument = (%s, %s)", contractHash, registrationHash, wantContract, wantRegistration)
	}
	if !strings.HasPrefix(contractHash, HashPrefix) || contractHash == registrationHash {
		t.Errorf("hashes = %s, %s", contractHash, registrationHash)
	}
	if h, err := Hash(doc); err != nil || h != contractHash {
		t.Errorf("Hash() = %s, %v; want the contractHash", h, err)
	}
}

func TestHashes_UnmarshalableDocumentFails(t *testing.T) {
	doc := &Document{Data: map[string]any{"bad": math.NaN()}}
	if _, _, err := Hashes(doc); err == nil {
		t.Error("expected an error")
	}
	if _, err := Hash(doc); err == nil {
		t.Error("expected an error")
	}
	if _, err := withHashes(doc.Data); err == nil {
		t.Error("expected an error")
	}
}
