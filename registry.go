package benzene

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Handler is a function from a request to a result (core-concepts.md §3). The handler
// never sees the transport - a transport binding maps its native payload to TReq and the
// Result[TRes] back to a native response.
type Handler[TReq, TRes any] func(ctx context.Context, req TReq) Result[TRes]

// erasedHandler is the type-erased form stored in the Registry, recovered via ResultInfo
// (see result.go) rather than the generic Result[TRes] the caller can't name. Its request
// parameter is `any` rather than TReq for the same reason: a transport binding resolves a
// handler by Topic alone and doesn't know TReq at compile time. See convertRequest for how
// the raw value becomes a typed TReq without needing reflect.Type bookkeeping at
// registration time.
type erasedHandler func(ctx context.Context, raw any) (ResultInfo, error)

// convertRequest implements the request-mapping rules of core-concepts.md §6:
//  1. If raw already *is* TReq, pass it through untouched (zero-copy) - this is what lets a
//     binding hand over its native type (e.g. *http.Request) for a handler that declares it
//     directly.
//  2. (Async-stream passthrough - not implemented by this MVP; see README's scope note.)
//  3. Otherwise, convert via JSON: raw must be []byte or json.RawMessage.
//
// This only works generically (without reflect.Type registration) because TReq is a concrete
// compile-time type argument at the Register call site - the closure Register builds already
// knows TReq statically, so ordinary Go generics do this, not reflection.
func convertRequest[TReq any](raw any) (TReq, error) {
	if typed, ok := raw.(TReq); ok {
		return typed, nil
	}

	var data []byte
	switch v := raw.(type) {
	case []byte:
		data = v
	case json.RawMessage:
		data = v
	default:
		var zero TReq
		return zero, fmt.Errorf("benzene: cannot convert request of type %T into %T", raw, zero)
	}

	var typed TReq
	if err := json.Unmarshal(data, &typed); err != nil {
		var zero TReq
		return zero, fmt.Errorf("benzene: failed to unmarshal request body into %T: %w", zero, err)
	}
	return typed, nil
}

// Registry holds (topic -> handler) registrations.
//
// The concept behind handler discovery is explicit registration (core-concepts.md §9);
// Register is that explicit path, and is the ONLY mechanism this Go port provides. Go has
// no reflection-based assembly-scanning culture equivalent to C#'s [Message("topic")]
// attribute scanning, and core-concepts §9 already requires explicit registration to be a
// first-class path in every language regardless - so there is nothing to defer to later
// here, this is simply the Go idiom.
type Registry struct {
	handlers map[Topic]erasedHandler
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[Topic]erasedHandler)}
}

// Register adds handler for topic. Returns an error if topic is already registered -
// registering two handlers for the same (id, version) pair is a startup error, not a
// runtime dispatch ambiguity (core-concepts.md §2).
func Register[TReq, TRes any](r *Registry, topic Topic, handler Handler[TReq, TRes]) error {
	if _, exists := r.handlers[topic]; exists {
		return fmt.Errorf("benzene: handler already registered for topic %q", topic)
	}
	r.handlers[topic] = func(ctx context.Context, raw any) (ResultInfo, error) {
		typedReq, err := convertRequest[TReq](raw)
		if err != nil {
			return nil, fmt.Errorf("benzene: topic %q: %w", topic, err)
		}
		return handler(ctx, typedReq), nil
	}
	return nil
}

// resolve looks up the handler registered for topic. Topic matching is exact - the topic
// id and version travel as literal strings once resolved (core-concepts.md §2); any
// normalization a transport binding wants to apply happens before calling resolve.
func (r *Registry) resolve(topic Topic) (erasedHandler, bool) {
	h, ok := r.handlers[topic]
	return h, ok
}

// Has reports whether a handler is registered for topic.
func (r *Registry) Has(topic Topic) bool {
	_, ok := r.handlers[topic]
	return ok
}

// Topics returns every registered topic, sorted by ID then Version. This is the
// enumeration behind service self-description (the mesh package's Descriptor): explicit
// registration means the Registry is the complete, authoritative list of what this
// service serves, so a catalog derived from it cannot drift from the running code.
func (r *Registry) Topics() []Topic {
	topics := make([]Topic, 0, len(r.handlers))
	for topic := range r.handlers {
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
