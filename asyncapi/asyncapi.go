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
//     derived entirely from the descriptor - no extra input, no fabrication.
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
	Action  string        `json:"action"` // "receive" | "send"
	Channel Ref           `json:"channel"`
	Reply   *OperationRef `json:"reply,omitempty"`
}

// Ref is an AsyncAPI reference object ({"$ref": "#/channels/..."}).
type Ref struct {
	Ref string `json:"$ref"`
}

// OperationRef is the reply object: the channel the reply is published on.
type OperationRef struct {
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

	doc := &Document{
		AsyncAPI:           "3.0.0",
		ID:                 buildID(cfg.title),
		DefaultContentType: "application/json",
		Info:               Info{Title: cfg.title, Version: cfg.version, Description: cfg.description},
		Channels:           map[string]Channel{},
		Operations:         map[string]Operation{},
	}

	usedOps := map[string]bool{}

	// Receive side: every handled topic, derived from the descriptor.
	for _, topic := range desc.Topics {
		reqChannel := topic.ID
		doc.Channels[reqChannel] = Channel{
			Address:  topic.ID,
			Messages: map[string]Message{"request": {Name: "request", Payload: copySchema(topic.RequestSchema)}},
		}

		op := Operation{Action: "receive", Channel: Ref{Ref: channelRef(reqChannel)}}

		// A reply channel for the response, named <topic>:<suffix>.
		replyChannel := topic.ID + ":" + cfg.responseSuffix
		doc.Channels[replyChannel] = Channel{
			Address:  replyChannel,
			Messages: map[string]Message{"response": {Name: "response", Payload: copySchema(topic.ResponseSchema)}},
		}
		op.Reply = &OperationRef{Channel: Ref{Ref: channelRef(replyChannel)}}

		doc.Operations[uniqueOp("receive_"+operationSlug(topic.ID), usedOps)] = op
	}

	// Send side: caller-declared published events.
	for _, ev := range cfg.sentEvents {
		doc.Channels[ev.topic] = Channel{
			Address:  ev.topic,
			Messages: map[string]Message{"event": {Name: "event", Payload: copySchema(ev.payload)}},
		}
		doc.Operations[uniqueOp("send_"+operationSlug(ev.topic), usedOps)] = Operation{
			Action:  "send",
			Channel: Ref{Ref: channelRef(ev.topic)},
		}
	}

	return doc
}

// channelRef builds the JSON-Pointer reference to a channel. Benzene topic ids use ':' separators
// (e.g. "order:create"), which are valid in a JSON Pointer / URI fragment and need no escaping; a
// '/' (which Benzene topics do not use, like the openapi package's assumption about '{'/'}') would.
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

// buildID turns the service title into the document's urn id (urn:benzene:service:<slug>), matching
// the .NET builder.
func buildID(title string) string {
	slug := operationSlug(strings.ToLower(title))
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
