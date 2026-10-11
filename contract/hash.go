package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/daniellepelley/benzene-go/internal/jcs"
)

// HashPrefix starts both hashes.
const HashPrefix = "sha256:"

// HashDocument computes a Contract Document's two hashes (contract-document.md §6) from its JSON:
//
//	contractHash     = hash({requests, events})
//	registrationHash = hash({requests, events, sends})
//	hash(P)          = "sha256:" + lowercase-hex(sha256(JCS(P)))
//
// where P is the §6.2 projection (see Projection) and JCS is RFC 8785. Any document can be
// hashed, whoever produced it; the document's own contractHash and registrationHash fields are
// outside P, so a stale value never feeds the new one.
func HashDocument(raw []byte) (contractHash, registrationHash string, err error) {
	doc, err := decodeGeneric(raw)
	if err != nil {
		return "", "", err
	}
	if contractHash, err = hashOf(Projection(doc, false)); err != nil {
		return "", "", err
	}
	if registrationHash, err = hashOf(Projection(doc, true)); err != nil {
		return "", "", err
	}
	return contractHash, registrationHash, nil
}

// decodeGeneric decodes a JSON object into a generic tree, keeping numbers as json.Number so the
// canonical form is taken from the literal's exact value rather than a float64 round trip.
func decodeGeneric(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("contract: hash: decode document: %w", err)
	}
	if doc == nil {
		return nil, errors.New("contract: hash: the document is not a JSON object")
	}
	return doc, nil
}

func hashOf(p map[string]any) (string, error) {
	canonical, err := jcs.Append(nil, p)
	if err != nil {
		return "", fmt.Errorf("contract: hash: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return HashPrefix + hex.EncodeToString(sum[:]), nil
}

// entryMembers are the members each list keeps in P (§6.2 step 2); schemaMembers are the ones that
// are schema positions.
var entryMembers = map[string][]string{
	"requests": {"topic", "version", "httpMappings", "request", "response"},
	"events":   {"topic", "version", "message"},
	"sends":    {"topic", "version", "request", "response"},
}

var schemaMembers = map[string]bool{"request": true, "response": true, "message": true}

// annotations are the keywords §6.2 step 4 removes at every schema position, besides every keyword
// starting "x-".
var annotations = map[string]bool{
	"title": true, "description": true, "default": true, "examples": true, "example": true,
	"deprecated": true, "readOnly": true, "writeOnly": true, "$comment": true, "$id": true,
	"$schema": true, "format": true,
}

// Projection builds the hash projection P of a generic JSON document (contract-document.md §6.2):
// requests and events - plus sends, always present, when withSends is set (the registrationHash
// projection) - with benzene: entries dropped, only the allow-listed members kept, every $ref
// dereferenced (a cycle cut to {}), annotations removed, required sorted, and entries and
// httpMappings in canonical order. The input is not modified.
func Projection(doc map[string]any, withSends bool) map[string]any {
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)

	lists := []string{"requests", "events"}
	if withSends {
		lists = append(lists, "sends")
	}
	p := make(map[string]any, len(lists))
	for _, list := range lists {
		raw, _ := doc[list].([]any)
		entries := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			topic, _ := entry["topic"].(string)
			if IsReserved(topic) {
				continue
			}
			projected := map[string]any{}
			for _, member := range entryMembers[list] {
				value, present := entry[member]
				if !present {
					continue
				}
				switch {
				case schemaMembers[member]:
					projected[member], _ = normalizeSchema(value, schemas, nil)
				case member == "httpMappings":
					projected[member] = normalizeMappings(value)
				default:
					projected[member] = value
				}
			}
			entries = append(entries, projected)
		}
		sort.SliceStable(entries, func(i, j int) bool { return entryLess(entries[i], entries[j]) })
		generic := make([]any, len(entries))
		for i, e := range entries {
			generic[i] = e
		}
		p[list] = generic
	}
	return p
}

// entryLess orders entries by topic, then version with an absent version first (§6.2 step 5).
func entryLess(a, b map[string]any) bool {
	ta, _ := a["topic"].(string)
	tb, _ := b["topic"].(string)
	if ta != tb {
		return lessUTF16(ta, tb)
	}
	va, hasA := a["version"]
	vb, hasB := b["version"]
	if hasA != hasB {
		return !hasA
	}
	sa, _ := va.(string)
	sb, _ := vb.(string)
	return lessUTF16(sa, sb)
}

// normalizeMappings upper-cases each mapping's method and sorts the mappings by (method, path),
// keeping only those two members.
func normalizeMappings(value any) any {
	raw, ok := value.([]any)
	if !ok {
		return value
	}
	mappings := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		method, _ := m["method"].(string)
		path, _ := m["path"].(string)
		mappings = append(mappings, map[string]any{"method": strings.ToUpper(method), "path": path})
	}
	sort.SliceStable(mappings, func(i, j int) bool {
		mi, mj := mappings[i]["method"].(string), mappings[j]["method"].(string)
		if mi != mj {
			return lessUTF16(mi, mj)
		}
		return lessUTF16(mappings[i]["path"].(string), mappings[j]["path"].(string))
	})
	out := make([]any, len(mappings))
	for i, m := range mappings {
		out[i] = m
	}
	return out
}

// normalizeSchema applies §6.2 steps 3-4 to the schema at one position. path holds the components
// being expanded on the current path; cut reports that the schema was a reference cut at a cycle,
// which is what lets the enclosing nullable wrapper collapse to {} too.
func normalizeSchema(value any, schemas map[string]any, path []string) (out any, cut bool) {
	s, ok := value.(map[string]any)
	if !ok {
		return value, false // not a schema object: kept as written
	}
	if ref, isRef := s["$ref"]; isRef {
		refStr, _ := ref.(string)
		name, prefixed := strings.CutPrefix(refStr, RefPrefix)
		if !prefixed {
			return map[string]any{}, false
		}
		for _, expanding := range path {
			if expanding == name {
				return map[string]any{}, true
			}
		}
		target, found := schemas[name]
		if !found {
			return map[string]any{}, false
		}
		normalized, _ := normalizeSchema(target, schemas, append(path[:len(path):len(path)], name))
		return normalized, false
	}

	result := make(map[string]any, len(s))
	var wrapsCut bool
	for key, v := range s {
		if annotations[key] || strings.HasPrefix(key, "x-") {
			continue
		}
		switch key {
		case "properties":
			props, ok := v.(map[string]any)
			if !ok {
				result[key] = v
				continue
			}
			if len(props) == 0 {
				continue
			}
			normalized := make(map[string]any, len(props))
			for name, prop := range props {
				normalized[name], _ = normalizeSchema(prop, schemas, path)
			}
			result[key] = normalized
		case "items", "additionalProperties", "not":
			result[key], _ = normalizeSchema(v, schemas, path)
		case "allOf", "anyOf", "oneOf":
			members, ok := v.([]any)
			if !ok {
				result[key] = v
				continue
			}
			normalized := make([]any, len(members))
			for i, m := range members {
				var memberCut bool
				normalized[i], memberCut = normalizeSchema(m, schemas, path)
				if key == "allOf" && len(members) == 1 && memberCut {
					wrapsCut = true
				}
			}
			result[key] = normalized
		case "required":
			required, ok := sortedStrings(v)
			if !ok {
				result[key] = v
				continue
			}
			if len(required) > 0 {
				result[key] = required
			}
		case "discriminator":
			d, ok := v.(map[string]any)
			if !ok {
				result[key] = v
				continue
			}
			kept := make(map[string]any, len(d))
			for dk, dv := range d {
				if dk != "mapping" {
					kept[dk] = dv
				}
			}
			result[key] = kept
		default:
			result[key] = v
		}
	}

	if wrapsCut && isNullableWrapper(result) {
		return map[string]any{}, false
	}
	return result, false
}

// isNullableWrapper reports whether s - which the caller knows has a one-member allOf - has exactly
// the members nullable: true, an optional type, and that allOf: the §4.2 nullable wrapper shape.
func isNullableWrapper(s map[string]any) bool {
	if s["nullable"] != true {
		return false
	}
	for key := range s {
		if key != "nullable" && key != "allOf" && key != "type" {
			return false
		}
	}
	return true
}

// sortedStrings returns v's elements sorted by UTF-16 code units when v is an array of strings.
func sortedStrings(v any) ([]any, bool) {
	raw, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(raw))
	for i, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	sort.SliceStable(out, func(i, j int) bool { return lessUTF16(out[i], out[j]) })
	generic := make([]any, len(out))
	for i, s := range out {
		generic[i] = s
	}
	return generic, true
}

// lessUTF16 compares two strings by their UTF-16 code units, the order JCS sorts member names in
// and §6.2 sorts everything else in.
func lessUTF16(a, b string) bool {
	if a == b {
		return false
	}
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
