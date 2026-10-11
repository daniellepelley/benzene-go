package benzene

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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

// handlerRegistration is what the Registry stores per topic: the type-erased handler plus the
// concrete request/response types captured at the Register call site (where TReq/TRes are
// statically known and free to record - they are unrecoverable from the erased closure
// afterwards). The types exist for startup-time introspection only - the mesh package
// derives JSON Schemas from them (work/archive/mesh.md Phase 2); dispatch never touches
// them and still recovers concrete results via ResultInfo, not reflection.
type handlerRegistration struct {
	handler  erasedHandler
	request  reflect.Type
	response reflect.Type
}

// Registry holds (topic -> handler) registrations.
//
// The concept behind handler discovery is explicit registration (core-concepts.md §9);
// Register is that explicit path, and is the ONLY mechanism this Go port provides. Go has
// no reflection-based assembly-scanning culture equivalent to C#'s [Message("topic")]
// attribute scanning, and core-concepts §9 already requires explicit registration to be a
// first-class path in every language regardless - so there is nothing to defer to later
// here, this is simply the Go idiom.
//
// The zero value is an empty Registry ready to use; NewRegistry is the same thing spelled as a
// constructor.
type Registry struct {
	handlers map[Topic]handlerRegistration
	// versions counts the registrations per topic id, so the router can tell a topic nobody serves
	// (an unmatched topic, transport-bindings.md §1 obligation 8) from a version nobody serves.
	versions map[string]int
	// defaults holds the version each topic id declared as its default handler (AsDefault).
	defaults map[string]string
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// RegisterOption configures one Register call.
type RegisterOption func(*registerConfig)

type registerConfig struct {
	asDefault bool
}

// AsDefault declares the handler being registered as its topic's default handler: the one a message
// that carries no version goes to (core-concepts.md §2). A topic whose handlers are all versioned
// needs one, or every unversioned message to it is not-found; declaring the oldest version the topic
// still accepts is what keeps producers that predate versioning working (versioning.md §2.2).
//
//	benzene.MustRegister(registry, benzene.NewTopic("order:create").WithVersion("v1"), createV1, benzene.AsDefault())
//	benzene.MustRegister(registry, benzene.NewTopic("order:create").WithVersion("v2"), createV2)
//
// An unversioned handler is always its topic's default, so AsDefault on an unversioned topic changes
// nothing, and a declared default is only used while the topic has no unversioned handler. Declaring
// two versions of one topic default is a registration error.
func AsDefault() RegisterOption {
	return func(c *registerConfig) { c.asDefault = true }
}

// Register adds handler for topic. Returns an error if topic is already registered -
// registering two handlers for the same (id, version) pair is a startup error, not a
// runtime dispatch ambiguity (core-concepts.md §2) - or if AsDefault declares a second default
// for the topic id.
func Register[TReq, TRes any](r *Registry, topic Topic, handler Handler[TReq, TRes], opts ...RegisterOption) error {
	var cfg registerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if _, exists := r.handlers[topic]; exists {
		return fmt.Errorf("benzene: handler already registered for topic %q", topic)
	}
	declareDefault := cfg.asDefault && topic.Version != ""
	if existing, ok := r.defaults[topic.ID]; ok && declareDefault {
		return fmt.Errorf("benzene: topic %q already declares version %q as its default; cannot also declare %q", topic.ID, existing, topic.Version)
	}

	if r.handlers == nil {
		r.handlers = make(map[Topic]handlerRegistration)
		r.versions = make(map[string]int)
		r.defaults = make(map[string]string)
	}
	r.handlers[topic] = handlerRegistration{
		handler: func(ctx context.Context, raw any) (ResultInfo, error) {
			typedReq, err := convertRequest[TReq](raw)
			if err != nil {
				return nil, fmt.Errorf("benzene: topic %q: %w", topic, err)
			}
			return handler(ctx, typedReq), nil
		},
		// TypeOf on a pointer's Elem rather than on a zero TReq value, so interface type
		// parameters yield their interface type instead of nil.
		request:  reflect.TypeOf((*TReq)(nil)).Elem(),
		response: reflect.TypeOf((*TRes)(nil)).Elem(),
	}
	r.versions[topic.ID]++
	if declareDefault {
		r.defaults[topic.ID] = topic.Version
	}
	return nil
}

// selection is why a lookup did or did not find a handler.
type selection int

const (
	selected       selection = iota
	unknownTopic             // no handler is registered for the topic id at any version
	unknownVersion           // the topic id has handlers, but none at the message's version
	noDefault                // no version was sent and the topic has no default handler
)

// lookup selects the handler for topic per core-concepts.md §2. A message carrying a version matches
// exactly: this port ships no payload casting, so there is no cast-reachable step, and there is never
// a "nearest" or "highest available" fallback - serving another version's behaviour to a caller that
// named a version is the failure versioning exists to prevent. A message without one goes to the
// topic's default handler: the unversioned handler, else the declared default (AsDefault). Topic ids
// and versions are literal strings once resolved; any normalization a transport wants happens first.
func (r *Registry) lookup(topic Topic) (erasedHandler, selection) {
	if reg, ok := r.handlers[topic]; ok {
		return reg.handler, selected
	}
	if r.versions[topic.ID] == 0 {
		return nil, unknownTopic
	}
	if topic.Version != "" {
		return nil, unknownVersion
	}
	if version, ok := r.defaults[topic.ID]; ok {
		return r.handlers[topic.WithVersion(version)].handler, selected
	}
	return nil, noDefault
}

// DefaultVersion reports which handler a message without a version is routed to for topic id: "" for
// the unversioned handler, the declared version for an AsDefault registration. ok is false when the
// topic is unknown, or when every handler it has is versioned and none is declared default.
func (r *Registry) DefaultVersion(id string) (version string, ok bool) {
	if _, unversioned := r.handlers[NewTopic(id)]; unversioned {
		return "", true
	}
	version, ok = r.defaults[id]
	return version, ok
}

// TopicsWithoutDefault returns, sorted, the ids of topics whose handlers are all versioned with none
// declared default (AsDefault). Such a topic is valid, but every message to it that carries no version
// is not-found - so core-concepts.md §2 asks for it to be reported at start-up, which RouterMiddleware
// does when it is built. Call it from a start-up check or a test to fail fast instead.
func (r *Registry) TopicsWithoutDefault() []string {
	var ids []string
	for id := range r.versions {
		if _, ok := r.DefaultVersion(id); !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// TopicTypes returns the request and response types captured when topic's handler was
// registered (reflect.TypeOf TReq and TRes), or ok = false when topic isn't registered.
// Startup-time introspection for service self-description - not a dispatch mechanism.
func (r *Registry) TopicTypes(topic Topic) (request, response reflect.Type, ok bool) {
	reg, ok := r.handlers[topic]
	return reg.request, reg.response, ok
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

// MustRegister is Register with the error handling a composition root would otherwise write by
// hand: it calls Register and panics if registration fails. The explicit form is Register -
// reach for it when the caller has somewhere better to send the error than a panic (a
// registration loop that collects failures, a plugin host, a test).
//
// The panic is a start-up check, not a runtime hazard: the only way Register fails is a
// duplicate (id, version) topic, which is a wiring mistake, and ConfigureServices runs once at
// boot before any message is handled (core-concepts.md §7). The panic therefore names the
// offending topic and happens at start-up, never on the message path - the same trade
// regexp.MustCompile and template.Must make.
//
// Explicit:
//
//	if err := benzene.Register(registry, benzene.NewTopic("greet"), greetHandler); err != nil {
//		return err
//	}
//
// Shorthand:
//
//	benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
//
// In both forms TReq/TRes are inferred from handler's signature; the explicit
// benzene.Handler[TReq, TRes](fn) conversion is never required for a function that already has
// the handler shape.
func MustRegister[TReq, TRes any](r *Registry, topic Topic, handler Handler[TReq, TRes], opts ...RegisterOption) {
	if err := Register(r, topic, handler, opts...); err != nil {
		panic(err)
	}
}
