package mesh

import (
	"fmt"
	"reflect"
	"sort"

	benzene "github.com/daniellepelley/benzene-go"
)

// OutboundRegistry holds outbound registration records (mesh.md §2.3): topics this service
// *may send*, with the request type it sends and the response type it expects back - no
// handler, since nothing here receives. It mirrors Registry's handler discovery exactly, minus
// the handler, for the identical reason core-concepts.md §9 requires explicit registration for
// inbound: the list this type holds is what makes ServiceDescriptor.Consumes a hard-coded
// contract rather than an inference. A port MUST NOT populate it by scanning call sites, string
// literals, or any other static analysis over handler bodies - RegisterOutbound is the only path.
//
// A registered entry needs no destination address, queue name, or topic ARN: those are
// transport/deployment configuration (transport-bindings.md), orthogonal to the contract this
// registers. A service can declare it consumes payments:capture while its actual queue URL is
// injected at deploy time - the descriptor doesn't change between environments, only the wiring
// does.
type OutboundRegistry struct {
	entries map[benzene.Topic]outboundEntry
}

// outboundEntry is what the OutboundRegistry stores per topic: the request/response types
// captured at the RegisterOutbound call site, for startup-time schema derivation only (mesh.go's
// Describe), exactly like handlerRegistration's types in registry.go.
type outboundEntry struct {
	request  reflect.Type
	response reflect.Type
}

// NewOutboundRegistry returns an empty OutboundRegistry.
func NewOutboundRegistry() *OutboundRegistry {
	return &OutboundRegistry{entries: make(map[benzene.Topic]outboundEntry)}
}

// RegisterOutbound records topic as a message this service may send: the request type it sends
// as TReq, and the response type it expects back as TRes (mesh.md §2.3). A sender with no
// expected response type registers TRes as `any` - schema derivation already maps an interface
// type to `{}` (unconstrained), which is exactly the responseSchema mesh.md §2 specifies for "no
// declared response type".
//
// Returns an error if topic is already registered - the same startup-error treatment
// Register gives a duplicate inbound registration.
func RegisterOutbound[TReq, TRes any](r *OutboundRegistry, topic benzene.Topic) error {
	if _, exists := r.entries[topic]; exists {
		return fmt.Errorf("mesh: outbound topic already registered for %q", topic)
	}
	r.entries[topic] = outboundEntry{
		// TypeOf on a pointer's Elem, matching registry.go's Register: interface type
		// parameters (including TRes = any) yield their interface type rather than nil.
		request:  reflect.TypeOf((*TReq)(nil)).Elem(),
		response: reflect.TypeOf((*TRes)(nil)).Elem(),
	}
	return nil
}

// TopicTypes returns the request and response types captured when topic was registered, or
// ok = false when topic isn't registered. Mirrors Registry.TopicTypes.
func (r *OutboundRegistry) TopicTypes(topic benzene.Topic) (request, response reflect.Type, ok bool) {
	entry, ok := r.entries[topic]
	return entry.request, entry.response, ok
}

// Topics returns every registered outbound topic, sorted by ID then Version - mirrors
// Registry.Topics, the enumeration behind Descriptor.Consumes.
func (r *OutboundRegistry) Topics() []benzene.Topic {
	topics := make([]benzene.Topic, 0, len(r.entries))
	for topic := range r.entries {
		topics = append(topics, topic)
	}
	sort.Slice(topics, func(i, j int) bool {
		if topics[i].ID != topics[j].ID {
			return topics[i].ID < topics[j].ID
		}
		return topics[i].Version < topics[j].Version
	})
	return topics
}
