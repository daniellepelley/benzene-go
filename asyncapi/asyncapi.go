// Package asyncapi derives an AsyncAPI 3.0 document from a Benzene service's registered topics - the
// event-driven sibling of the openapi package and the Go form of Benzene.Schema.OpenApi's AsyncAPI
// half. Like openapi it is zero-dependency and reuses the request/response JSON Schemas the mesh
// package already derives from each handler's TReq/TRes types at startup (the one sanctioned use of
// reflection, never on the dispatch path); it adds no reflection of its own.
//
// # The model, and how it resolves the request/response-vs-event classification
//
// AsyncAPI 3.0 separates channels (addressable message containers) from operations (what the
// application does with them), naming the direction explicitly with action "receive" / "send" from
// the application's own perspective - so, unlike 2.x's publish/subscribe, there is no ambiguity.
// This port maps Benzene onto that model exactly as the .NET builder does:
//
//   - Every registered topic is something the service RECEIVES: each becomes a channel carrying the
//     request message, plus a "receive" operation whose reply is the native AsyncAPI reply object
//     pointing at a reply channel named "<topic>:<responseSuffix>" (default "response"). This half is
//     derived entirely from the descriptor - no extra input, no fabrication. (Because the descriptor
//     does not distinguish request/response from fire-and-forget topics, a reply channel is added for
//     every handled topic; for a genuinely fire-and-forget handler that reply channel is a
//     documentation artifact, not a guarantee the topic is request/response - the same limitation the
//     openapi package notes.)
//   - What a service SENDS (a fire-and-forget event it publishes, e.g. via responseevents) is NOT in
//     the descriptor - the registry only knows what a service handles, not what it emits. So sent
//     events are a caller-declared input: WithSentEvent(topic, payloadSchema) adds a channel + a
//     "send" operation. This mirrors the .NET builder consuming the app's broadcast-event / message-
//     sender definitions, and is why the earlier openapi package deferred AsyncAPI: the send side
//     needs a declaration, which this package takes explicitly rather than inventing.
//
// A service with only handlers gets a complete receive-side document with no sent events; a service
// that also declares its published events gets both directions.
//
// Payload schemas are inlined as AsyncAPI Schema objects. AsyncAPI 3.0's schema format is JSON Schema
// (Draft 7), which - unlike OpenAPI 3.0 - permits the nullable type array (["string","null"]) mesh
// derives, so the derived schemas are used as-is (deep-copied so Generate never mutates the
// descriptor), with none of openapi's nullable reshaping.
package asyncapi

import (
	"strconv"
	"strings"

	"github.com/daniellepelley/benzene-go/mesh"
)

// Document is a minimal AsyncAPI 3.0 document: enough of the shape to describe a Benzene service's
// received topics (with their replies) and any declared sent events.
type Document struct {
	AsyncAPI           string               `json:"asyncapi"`
	ID                 string               `json:"id,omitempty"`
	DefaultContentType string               `json:"defaultContentType"`
	Info               Info                 `json:"info"`
	Channels           map[string]Channel   `json:"channels"`
	Operations         map[string]Operation `json:"operations"`
}

// Info is the AsyncAPI info object: the service's identity.
type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// Channel is an addressable message container. Its messages are keyed by a local name.
type Channel struct {
	Address  string             `json:"address"`
	Messages map[string]Message `json:"messages"`
}

// Message carries a payload schema (inlined; AsyncAPI 3.0 payloads are JSON Schema).
type Message struct {
	Name    string         `json:"name,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Operation is what the application does with a channel: receive (an inbound handler) or send (a
// declared outbound event). A receive operation carries a reply referencing the reply channel.
type Operation struct {
	Action  string `json:"action"` // "receive" | "send"
	Channel Ref    `json:"channel"`
	Reply   *Reply `json:"reply,omitempty"`
}

// Ref is an AsyncAPI reference object ({"$ref": "#/channels/..."}).
type Ref struct {
	Ref string `json:"$ref"`
}

// Reply is an AsyncAPI 3.0 Operation Reply Object: the channel a receive operation's reply is
// published on.
type Reply struct {
	Channel Ref `json:"channel"`
}

type sentEvent struct {
	topic   string
	payload map[string]any
}

type config struct {
	title          string
	version        string
	description    string
	responseSuffix string
	sentEvents     []sentEvent
}

// Option configures Generate.
type Option func(*config)

// WithTitle overrides the document title (default: the descriptor's service name, or
// "benzene-service" when that is empty).
func WithTitle(title string) Option { return func(c *config) { c.title = title } }

// WithVersion overrides the document version (default: the descriptor's service version, or "0.0.0"
// when that is empty).
func WithVersion(version string) Option { return func(c *config) { c.version = version } }

// WithDescription sets the info description.
func WithDescription(description string) Option {
	return func(c *config) { c.description = description }
}

// WithResponseTopicSuffix sets the suffix appended to a received topic to name its reply channel's
// address ("<topic>:<suffix>"). Default "response", so a handler on "shipping:get-all" replies on
// "shipping:get-all:response" - matching the .NET AsyncApiSpecOptions default. An empty suffix is
// ignored (the default is kept), since a reply channel must have a distinct address.
func WithResponseTopicSuffix(suffix string) Option {
	return func(c *config) {
		if strings.TrimSpace(suffix) != "" {
			c.responseSuffix = suffix
		}
	}
}

// WithSentEvent declares that the service publishes a fire-and-forget event on topic carrying
// payload (a JSON Schema, e.g. from mesh's derived schema for the event type, or nil when the event
// has no body). It adds a channel and a "send" operation. Repeatable; a repeated topic replaces the
// earlier declaration. This is the caller-supplied send side the descriptor cannot provide.
func WithSentEvent(topic string, payload map[string]any) Option {
	return func(c *config) {
		for i := range c.sentEvents {
			if c.sentEvents[i].topic == topic {
				c.sentEvents[i].payload = payload
				return
			}
		}
		c.sentEvents = append(c.sentEvents, sentEvent{topic: topic, payload: payload})
	}
}

// DefaultResponseTopicSuffix is the default reply-channel address suffix (see WithResponseTopicSuffix).
const DefaultResponseTopicSuffix = "response"

// Generate builds an AsyncAPI 3.0 document from desc (typically mesh.Describe(registry, info)). Each
// registered topic becomes a receive operation with a reply channel; each WithSentEvent becomes a
// send operation. The result marshals to valid AsyncAPI 3.0 JSON with encoding/json; map keys marshal
// sorted, so the output is deterministic for a given descriptor and option set.
func Generate(desc mesh.Descriptor, opts ...Option) *Document {
	cfg := config{title: desc.Service, version: desc.ServiceVersion, responseSuffix: DefaultResponseTopicSuffix}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.title == "" {
		cfg.title = "benzene-service"
	}
	if cfg.version == "" {
		cfg.version = "0.0.0"
	}

	book := newChannelBook()
	operations := map[string]Operation{}
	usedOps := map[string]bool{}

	// Receive side: every handled topic, derived from the descriptor. Each is a receive operation on
	// the request channel, replying on a "<topic>:<suffix>" channel.
	for _, topic := range desc.Topics {
		reqKey := book.addMessage(topic.ID, "request", Message{Name: "request", Payload: copySchema(topic.RequestSchema)})
		replyKey := book.addMessage(topic.ID+":"+cfg.responseSuffix, "response", Message{Name: "response", Payload: copySchema(topic.ResponseSchema)})
		operations[uniqueOp("receive_"+operationSlug(topic.ID), usedOps)] = Operation{
			Action:  "receive",
			Channel: Ref{Ref: channelRef(reqKey)},
			Reply:   &Reply{Channel: Ref{Ref: channelRef(replyKey)}},
		}
	}

	// Send side: caller-declared published events. Reusing the channel book by address means a topic
	// that is BOTH handled and declared as a sent event becomes one channel carrying both the request
	// and the event message, not two channels or a silent overwrite (matching the .NET builder).
	for _, ev := range cfg.sentEvents {
		evKey := book.addMessage(ev.topic, "event", Message{Name: "event", Payload: copySchema(ev.payload)})
		operations[uniqueOp("send_"+operationSlug(ev.topic), usedOps)] = Operation{
			Action:  "send",
			Channel: Ref{Ref: channelRef(evKey)},
		}
	}

	return &Document{
		AsyncAPI:           "3.0.0",
		ID:                 buildID(cfg.title),
		DefaultContentType: "application/json",
		Info:               Info{Title: cfg.title, Version: cfg.version, Description: cfg.description},
		Channels:           book.channels,
		Operations:         operations,
	}
}

// channelBook builds the channels map. It keeps channel MAP KEYS sanitized - AsyncAPI 3.0 keys must
// match [A-Za-z0-9._-], so the raw topic id (which may contain ':') is kept only as the channel's
// address - and reuses a channel by address, so a topic that is both handled and published (or the
// same address in two roles) becomes ONE channel carrying both messages rather than a duplicate or a
// silent overwrite. This mirrors the .NET builder's GetOrAddChannel / SanitizeKey.
type channelBook struct {
	byAddress map[string]string // address -> sanitized channel key
	usedKeys  map[string]bool
	channels  map[string]Channel
}

func newChannelBook() *channelBook {
	return &channelBook{byAddress: map[string]string{}, usedKeys: map[string]bool{}, channels: map[string]Channel{}}
}

// keyFor returns the stable sanitized map key for address, allocating one on first use. A sanitized
// key that would collide with a DIFFERENT address gets a "_2", "_3", ... suffix, so distinct
// addresses never share a channel.
func (b *channelBook) keyFor(address string) string {
	if key, ok := b.byAddress[address]; ok {
		return key
	}
	base := operationSlug(address)
	if base == "" {
		base = "channel"
	}
	key := base
	for n := 2; b.usedKeys[key]; n++ {
		key = base + "_" + strconv.Itoa(n)
	}
	b.usedKeys[key] = true
	b.byAddress[address] = key
	b.channels[key] = Channel{Address: address, Messages: map[string]Message{}}
	return key
}

// addMessage adds msg (under name) to the channel for address, creating the channel on first use, and
// returns the channel's map key for building a $ref. Messages is a map, so mutating it through the
// map-value copy updates the stored channel in place.
func (b *channelBook) addMessage(address, name string, msg Message) string {
	key := b.keyFor(address)
	b.channels[key].Messages[name] = msg
	return key
}

// channelRef builds the JSON-Pointer reference to a channel by its (sanitized) map key.
func channelRef(channelKey string) string {
	return "#/channels/" + channelKey
}

// copySchema deep-copies a derived JSON Schema so Generate never mutates the descriptor's maps
// (Generate must be safe to call repeatedly on the same descriptor). Unlike openapi.toOpenAPISchema
// it does no reshaping: AsyncAPI 3.0 schemas are JSON Schema, so mesh's derived form is already
// valid. A nil schema (a topic/event with no body) copies to nil.
func copySchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = copyValue(v)
	}
	return out
}

func copyValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return copySchema(typed)
	case []any:
		out := make([]any, len(typed))
		for i, e := range typed {
			out[i] = copyValue(e)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	default:
		return v
	}
}

// buildID turns the service title into the document's urn id (urn:benzene:service:<slug>). It matches
// the .NET builder's BuildId exactly - lowercase, each non-alphanumeric character mapped to '-' (runs
// are NOT collapsed), trimmed of leading/trailing '-' - so the same service gets the same document id
// across ports.
func buildID(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "urn:benzene:service"
	}
	return "urn:benzene:service:" + slug
}

// uniqueOp guarantees a unique operation key: a collision (two topics that slug to the same) gets a
// "_2", "_3", ... suffix, in descriptor order, so the document stays valid and deterministic. base is
// always non-empty here (callers pass a "receive_"/"send_" prefix), so an all-non-alphanumeric topic
// still yields a valid key ("receive_", then "receive__2", ...).
func uniqueOp(base string, used map[string]bool) string {
	key := base
	for n := 2; used[key]; n++ {
		key = base + "_" + strconv.Itoa(n)
	}
	used[key] = true
	return key
}

// operationSlug turns a topic id into a bare identifier for an operation key: non-alphanumeric runs
// become a single underscore ("order:create" -> "order_create"). It may return "" for an id with no
// alphanumerics; uniqueOp then supplies a fallback.
func operationSlug(topicID string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range topicID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
