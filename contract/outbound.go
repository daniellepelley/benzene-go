package contract

import (
	"fmt"
	"reflect"
	"sort"

	benzene "github.com/daniellepelley/benzene-go"
)

// OutboundKind says who owns an outbound topic's contract (contract-document.md §3).
type OutboundKind int

const (
	// KindEvent is a topic this service publishes and defines: part of what a client of this
	// service depends on, listed under the document's events.
	KindEvent OutboundKind = iota + 1
	// KindSend is a message to a topic another party defines - a command, a request to another
	// service, a publish onto someone else's topic: a dependency, listed under sends.
	KindSend
)

func (k OutboundKind) String() string {
	if k == KindEvent {
		return "event"
	}
	return "send"
}

// OutboundRegistry holds the service's event and send declarations (contract-document.md §3.3):
// (topic, version, types) records an application hands the framework at start-up, beside its
// handler registrations. It is the only source of the document's events and sends - nothing is
// inferred from call sites, string literals or any other analysis of code. A declaration names a
// topic, never a queue URL, ARN or other address: those are deployment configuration, and the
// document does not change between environments.
//
// Events and sends share one table, so a (topic, version) declared as both is the same duplicate
// error a second registration of either kind is.
type OutboundRegistry struct {
	entries map[benzene.Topic]outboundEntry
}

// outboundEntry is one declaration: its kind and the types captured at the call site, for
// start-up schema derivation only. response is nil for an event and for a one-way send.
type outboundEntry struct {
	kind     OutboundKind
	request  reflect.Type
	response reflect.Type
}

// NewOutboundRegistry returns an empty OutboundRegistry.
func NewOutboundRegistry() *OutboundRegistry {
	return &OutboundRegistry{entries: make(map[benzene.Topic]outboundEntry)}
}

func (r *OutboundRegistry) add(topic benzene.Topic, entry outboundEntry) error {
	if existing, ok := r.entries[topic]; ok {
		if existing.kind != entry.kind {
			return fmt.Errorf("contract: topic %q is already declared as %s %s; a (topic, version) cannot be both an event and a send", topic, article(existing.kind), existing.kind)
		}
		return fmt.Errorf("contract: %s already declared for topic %q", entry.kind, topic)
	}
	if r.entries == nil {
		r.entries = make(map[benzene.Topic]outboundEntry)
	}
	r.entries[topic] = entry
	return nil
}

func article(k OutboundKind) string {
	if k == KindEvent {
		return "an"
	}
	return "a"
}

// typeOf captures T at a generic call site; interface type arguments yield their interface type
// rather than nil (the same trick benzene.Register uses).
func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// RegisterEvent declares topic as an event this service publishes, with payload type T. Returns an
// error if topic (id and version) is already declared, as an event or as a send.
func RegisterEvent[T any](r *OutboundRegistry, topic benzene.Topic) error {
	return r.add(topic, outboundEntry{kind: KindEvent, request: typeOf[T]()})
}

// MustRegisterEvent is RegisterEvent that panics on error: a duplicate declaration is a wiring
// mistake, found at start-up before any message is sent - the same trade benzene.MustRegister
// makes.
//
//	contract.MustRegisterEvent[OrderPlaced](outbound, benzene.NewTopic("order:placed"))
func MustRegisterEvent[T any](r *OutboundRegistry, topic benzene.Topic) {
	if err := RegisterEvent[T](r, topic); err != nil {
		panic(err)
	}
}

// SendOption configures RegisterSend.
type SendOption func(*outboundEntry)

// WithResponse declares the type of the reply a send expects. Without it the send is one-way and
// its document entry has no response; WithResponse[any]() declares a reply of unconstrained shape.
func WithResponse[TRes any]() SendOption {
	return func(e *outboundEntry) { e.response = typeOf[TRes]() }
}

// RegisterSend declares topic as a message this service sends to a topic another party defines,
// with request type TReq: one-way unless WithResponse names the reply type. Returns an error if
// topic (id and version) is already declared, as a send or as an event.
//
//	contract.RegisterSend[TakePayment](outbound, benzene.NewTopic("payment:take"))
//	contract.RegisterSend[Reserve](outbound, benzene.NewTopic("inventory:reserve").WithVersion("v2"),
//		contract.WithResponse[Reservation]())
func RegisterSend[TReq any](r *OutboundRegistry, topic benzene.Topic, opts ...SendOption) error {
	entry := outboundEntry{kind: KindSend, request: typeOf[TReq]()}
	for _, opt := range opts {
		opt(&entry)
	}
	return r.add(topic, entry)
}

// MustRegisterSend is RegisterSend that panics on error (see MustRegisterEvent).
func MustRegisterSend[TReq any](r *OutboundRegistry, topic benzene.Topic, opts ...SendOption) {
	if err := RegisterSend[TReq](r, topic, opts...); err != nil {
		panic(err)
	}
}

// Kind reports whether topic is declared as an event or a send; ok is false when it is not
// declared.
func (r *OutboundRegistry) Kind(topic benzene.Topic) (kind OutboundKind, ok bool) {
	entry, ok := r.entries[topic]
	return entry.kind, ok
}

// TopicTypes returns the types captured when topic was declared: the event's payload or the
// send's request type, and the send's reply type - nil for an event or a one-way send. ok is
// false when topic is not declared.
func (r *OutboundRegistry) TopicTypes(topic benzene.Topic) (request, response reflect.Type, ok bool) {
	entry, ok := r.entries[topic]
	return entry.request, entry.response, ok
}

// Topics returns every declared topic, events and sends alike, sorted by id then version.
func (r *OutboundRegistry) Topics() []benzene.Topic { return r.topics(0) }

// Events returns the topics declared as events, sorted by id then version.
func (r *OutboundRegistry) Events() []benzene.Topic { return r.topics(KindEvent) }

// Sends returns the topics declared as sends, sorted by id then version.
func (r *OutboundRegistry) Sends() []benzene.Topic { return r.topics(KindSend) }

func (r *OutboundRegistry) topics(kind OutboundKind) []benzene.Topic {
	topics := make([]benzene.Topic, 0, len(r.entries))
	for topic, entry := range r.entries {
		if kind == 0 || entry.kind == kind {
			topics = append(topics, topic)
		}
	}
	sortTopics(topics)
	return topics
}

func sortTopics(topics []benzene.Topic) {
	sort.Slice(topics, func(i, j int) bool {
		if topics[i].ID != topics[j].ID {
			return topics[i].ID < topics[j].ID
		}
		return topics[i].Version < topics[j].Version
	})
}
