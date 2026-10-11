package contractdoc

import (
	"fmt"

	"github.com/daniellepelley/benzene-go/contract"
)

// HashPrefix starts both hashes.
const HashPrefix = contract.HashPrefix

// Hashes computes doc's contractHash and registrationHash (contract-document.md §6) - by the root
// module's contract.HashDocument, the same code a serving Benzene service hashes its own document
// with, so the value a generated client embeds and the value a provider serves come from one
// implementation. The hashes are of doc as it stands: hash a projection (ApplyScope,
// TopicScopedProjection) to get that projection's hashes.
func Hashes(doc *Document) (contractHash, registrationHash string, err error) {
	raw, err := doc.MarshalJSON()
	if err != nil {
		return "", "", fmt.Errorf("contractdoc: hash: %w", err)
	}
	return contract.HashDocument(raw)
}

// Hash returns doc's contractHash - the value a generated client embeds (contract-document.md
// §6.1).
func Hash(doc *Document) (string, error) {
	contractHash, _, err := Hashes(doc)
	return contractHash, err
}

// withHashes returns out with contractHash and registrationHash recomputed over it: a projection
// carries its own hashes, never the provider's (contract-document.md §5.2, §5.3).
func withHashes(out map[string]any) (*Document, error) {
	doc := &Document{Data: out}
	contractHash, registrationHash, err := Hashes(doc)
	if err != nil {
		return nil, err
	}
	out["contractHash"], out["registrationHash"] = contractHash, registrationHash
	return doc, nil
}
