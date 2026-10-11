package contract

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/jcs"
)

// NameCollisionError is Derive's failure when two payload types that describe different shapes
// claim one component name (contract-document.md §4.1 rule 4): it names the types, the topics that
// use them, and the remedy.
type NameCollisionError struct {
	// Name is the component name both types claim.
	Name string
	// Types are the colliding types' package-qualified names, sorted.
	Types []string
	// Topics are the entries that reach either type, sorted.
	Topics []string
}

func (e *NameCollisionError) Error() string {
	return fmt.Sprintf("contract: component name %q is claimed by %s (used by %s); "+
		"declare a distinct schema name on one of them with a BenzeneSchemaName() string method",
		e.Name, strings.Join(e.Types, " and "), strings.Join(e.Topics, ", "))
}

// Derive builds the service's Contract Document (contract-document.md) from its handler registry,
// its event and send declarations, and info. Call it once at start-up, after every registration
// and before any message is handled: it is where a component-name collision fails (as a
// *NameCollisionError, joined with any others), naming both types, so a service never starts with
// an ambiguous contract.
//
//   - requests come from registry: one entry per registered (topic, version), its request schema
//     from TReq and its response from TRes - absent when TRes is the unnamed struct{} (the handler
//     returns no payload), {} when TRes is an interface such as any;
//   - events and sends come from outbound; a nil outbound leaves them undetermined, so both are
//     listed empty and named in Degraded (an empty OutboundRegistry asserts "none");
//   - Runtime is "go"; Placement and Profile come from info;
//   - every named struct type is hoisted into components under its declared name (SchemaNamer) or
//     its Go type name; an undeclared generic instance is inlined;
//   - reserved benzene: topics are never listed;
//   - contractHash and registrationHash are computed over the result.
//
// info.Service is required, and registry must not be nil.
func Derive(registry *benzene.Registry, outbound *OutboundRegistry, info Info) (Document, error) {
	if info.Service == "" {
		return Document{}, errors.New("contract: Info.Service is required")
	}
	if registry == nil {
		return Document{}, errors.New("contract: a registry is required")
	}

	d := newDeriver()
	doc := Document{
		SchemaDialect:   SchemaDialect,
		Service:         info.Service,
		ServiceVersion:  info.ServiceVersion,
		VersionScheme:   info.VersionScheme,
		Description:     info.Description,
		Runtime:         Runtime,
		Placement:       info.Placement,
		Profile:         info.Profile,
		MessageEndpoint: info.MessageEndpoint,
		Transports:      append([]string(nil), info.Transports...),
		Requests:        []Request{},
		Events:          []Event{},
		Sends:           []Send{},
		Components:      Components{Schemas: map[string]Schema{}},
	}

	// uses records which entries reach each schema, for a collision's error message.
	type use struct {
		label   string
		schemas []Schema
	}
	var uses []use

	for _, topic := range registry.Topics() {
		if IsReserved(topic.ID) {
			continue
		}
		reqType, resType, _ := registry.TopicTypes(topic)
		entry := Request{
			Topic:        topic.ID,
			Version:      topic.Version,
			HTTPMappings: mappingsFor(topic, info.HTTPRoutes),
			Request:      d.schema(reqType, nil),
		}
		if !noPayload(resType) {
			entry.Response = d.schema(resType, nil)
		}
		doc.Requests = append(doc.Requests, entry)
		uses = append(uses, use{topic.String(), []Schema{entry.Request, entry.Response}})
	}

	if outbound == nil {
		doc.Degraded = []string{MemberEvents, MemberSends}
	} else {
		for _, topic := range outbound.Events() {
			if IsReserved(topic.ID) {
				continue
			}
			msgType, _, _ := outbound.TopicTypes(topic)
			entry := Event{Topic: topic.ID, Version: topic.Version, Message: d.schema(msgType, nil)}
			doc.Events = append(doc.Events, entry)
			uses = append(uses, use{topic.String(), []Schema{entry.Message}})
		}
		for _, topic := range outbound.Sends() {
			if IsReserved(topic.ID) {
				continue
			}
			reqType, resType, _ := outbound.TopicTypes(topic)
			entry := Send{Topic: topic.ID, Version: topic.Version, Request: d.schema(reqType, nil)}
			if resType != nil {
				entry.Response = d.schema(resType, nil)
			}
			doc.Sends = append(doc.Sends, entry)
			uses = append(uses, use{topic.String(), []Schema{entry.Request, entry.Response}})
		}
	}

	// Name the components. Keys sharing a name either describe one shape - and share one entry - or
	// collide.
	byName := map[string][]string{}
	for key, name := range d.names {
		byName[name] = append(byName[name], key)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	survivor := map[string]string{} // provisional key -> the key whose schema the name publishes
	var collisions []error
	for _, name := range names {
		keys := byName[name]
		sort.Strings(keys)
		if len(keys) > 1 && !sameShape(d.schemas, keys) {
			err := &NameCollisionError{Name: name}
			reached := map[string]bool{}
			for _, key := range keys {
				err.Types = append(err.Types, qualifiedName(d.types[key]))
				reached[key] = true
			}
			for _, u := range uses {
				if reachesAny(u.schemas, d.schemas, reached) {
					err.Topics = append(err.Topics, u.label)
				}
			}
			sort.Strings(err.Topics)
			collisions = append(collisions, err)
			continue
		}
		for _, key := range keys {
			survivor[key] = keys[0]
		}
	}
	if len(collisions) > 0 {
		return Document{}, errors.Join(collisions...)
	}

	rename := func(s Schema) Schema {
		if s == nil {
			return nil
		}
		return renameRefs(s, func(key string) string { return d.names[survivor[key]] }).(Schema)
	}
	for key, target := range survivor {
		if key == target {
			doc.Components.Schemas[d.names[key]] = rename(d.schemas[key])
		}
	}
	for i := range doc.Requests {
		doc.Requests[i].Request = rename(doc.Requests[i].Request)
		doc.Requests[i].Response = rename(doc.Requests[i].Response)
	}
	for i := range doc.Events {
		doc.Events[i].Message = rename(doc.Events[i].Message)
	}
	for i := range doc.Sends {
		doc.Sends[i].Request = rename(doc.Sends[i].Request)
		doc.Sends[i].Response = rename(doc.Sends[i].Response)
	}

	// A derived document always marshals and hashes - it holds strings and derived schemas, never a
	// number - so the error is unreachable here; it is returned rather than ignored all the same.
	contractHash, registrationHash, err := doc.Hashes()
	if err != nil {
		return Document{}, err
	}
	doc.ContractHash, doc.RegistrationHash = contractHash, registrationHash
	return doc, nil
}

// noPayload reports whether a handler's response type means "no payload": the unnamed empty
// struct. A named empty struct is still an object, and an interface is a payload of any shape.
func noPayload(t reflect.Type) bool {
	return t == nil || (t.Kind() == reflect.Struct && t.Name() == "" && t.NumField() == 0)
}

// mappingsFor returns the HTTP routes that reach topic: those naming it exactly, and unversioned
// routes to its id (which reach every version of it).
func mappingsFor(topic benzene.Topic, routes []HTTPRoute) []HTTPMapping {
	var out []HTTPMapping
	for _, r := range routes {
		if r.Topic.ID == topic.ID && (r.Topic.Version == "" || r.Topic.Version == topic.Version) {
			out = append(out, HTTPMapping{Method: strings.ToUpper(r.Method), Path: r.Path})
		}
	}
	return out
}

// sameShape reports whether the provisional components keys all describe one shape: identical
// once dereferenced and stripped of annotations (§6.2 steps 3-4), which is when §4.1 rule 4 lets
// them share a name.
func sameShape(schemas map[string]Schema, keys []string) bool {
	generic := make(map[string]any, len(schemas))
	for k, v := range schemas {
		generic[k] = v
	}
	var first []byte
	for i, key := range keys {
		normalized, _ := normalizeSchema(Schema{"$ref": RefPrefix + key}, generic, nil)
		// A derived schema holds only strings, booleans, maps and slices of them: it always
		// canonicalizes.
		canonical, _ := jcs.Append(nil, normalized)
		if i == 0 {
			first = canonical
		} else if !bytes.Equal(first, canonical) {
			return false
		}
	}
	return true
}

// reachesAny reports whether any of roots reaches, through $refs, a provisional key in targets.
func reachesAny(roots []Schema, schemas map[string]Schema, targets map[string]bool) bool {
	seen := map[string]bool{}
	var walk func(v any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			if ref, ok := t["$ref"].(string); ok {
				key := strings.TrimPrefix(ref, RefPrefix)
				if targets[key] {
					return true
				}
				if !seen[key] {
					seen[key] = true
					if walk(schemas[key]) {
						return true
					}
				}
			}
			for _, child := range t {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range t {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	for _, root := range roots {
		if walk(root) {
			return true
		}
	}
	return false
}

// renameRefs deep-copies a derived schema, rewriting each provisional $ref to its component name.
func renameRefs(v any, name func(key string) string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			if ref, ok := child.(string); ok && k == "$ref" {
				out[k] = RefPrefix + name(strings.TrimPrefix(ref, RefPrefix))
				continue
			}
			out[k] = renameRefs(child, name)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = renameRefs(child, name)
		}
		return out
	}
	return v // strings, []string (required), booleans
}
