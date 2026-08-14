package contractdoc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
)

// HashPrefix is contractHash's fixed prefix, matching descriptorHash's convention
// (mesh.md §2.2).
const HashPrefix = "sha256:"

// Normalize implements contract-document.md §6.2's normalize() step over doc's raw JSON tree:
// strips messageEndpoint/transports, every requests[]/events[] entry's "example", the "reserved"
// flag itself off every surviving requests[] entry, and - only when topicScoped is false, i.e.
// hashing a whole-service/service-level document rather than a §5.3 topic-scoped one - drops
// every requests[] entry §5.1 detects as reserved (flag OR "benzene:" prefix) entirely, not just
// its flag. Returns a new Document; doc itself is not mutated.
func Normalize(doc *Document, topicScoped bool) *Document {
	out := doc.clone()
	delete(out, "messageEndpoint")
	delete(out, "transports")

	if requests, ok := out["requests"].([]any); ok {
		kept := make([]any, 0, len(requests))
		for _, entry := range requests {
			request, ok := entry.(map[string]any)
			if !ok {
				kept = append(kept, entry)
				continue
			}
			request = shallowCopyEntry(request)
			delete(request, "example")

			// §6.2 requires reserved-ness to be evaluated (flag OR the "benzene:" prefix, §5.1)
			// BEFORE the "reserved" flag itself is stripped below.
			isReserved := IsReserved(request)
			delete(request, "reserved")

			if isReserved && !topicScoped {
				continue
			}
			kept = append(kept, request)
		}
		out["requests"] = kept
	}

	if events, ok := out["events"].([]any); ok {
		kept := make([]any, 0, len(events))
		for _, entry := range events {
			event, ok := entry.(map[string]any)
			if !ok {
				kept = append(kept, entry)
				continue
			}
			event = shallowCopyEntry(event)
			delete(event, "example")
			kept = append(kept, event)
		}
		out["events"] = kept
	}

	return &Document{Data: out}
}

func shallowCopyEntry(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Hash computes doc's contractHash (contract-document.md §6.2):
//
//	"sha256:" + lowercase-hex(sha256(canonicalJSON(normalize(document))))
//
// canonicalJSON is RFC 8785 (JCS), via github.com/gowebpki/jcs. topicScoped selects which of
// normalize()'s two reserved-entry behaviors applies - see Normalize's doc comment; pass true
// only for a §5.3 topic-scoped (single-topic) document, false for every other projection
// (whole-service, or a service-level client filtered by an include-list, §5.2).
func Hash(doc *Document, topicScoped bool) (string, error) {
	normalized, err := json.Marshal(Normalize(doc, topicScoped).Data)
	if err != nil {
		return "", fmt.Errorf("contractdoc: hash: marshal normalized document: %w", err)
	}

	canonical, err := jcs.Transform(normalized)
	if err != nil {
		return "", fmt.Errorf("contractdoc: hash: canonicalize: %w", err)
	}

	sum := sha256.Sum256(canonical)
	return HashPrefix + hex.EncodeToString(sum[:]), nil
}
