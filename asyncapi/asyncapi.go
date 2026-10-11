// Package asyncapi derives an AsyncAPI 3.0 document from a Benzene service's Contract Document - the
// event-driven sibling of the openapi package and the Go form of Benzene.Schema.OpenApi's AsyncAPI
// half. It is zero-dependency and adds no reflection: every schema comes from the contract package's
// start-up derivation.
//
// # The model
//
// AsyncAPI 3.0 separates channels (addressable message containers) from operations (what the
// application does with them), naming the direction explicitly with action "receive" / "send" from
// the application's own perspective. The Contract Document already says which is which, so this
// package only maps it:
//
//   - every requests[] entry is a topic the service RECEIVES: a channel carrying the request message
//     and a "receive" operation. When the entry has a response, the operation's native AsyncAPI reply
//     points at a reply channel named "<topic>:<responseSuffix>" (default "response"); a handler that
//     returns no payload has no reply.
//   - every events[] entry is an event the service publishes and defines: a "send" operation on a
//     channel carrying the "event" message.
//   - every sends[] entry is a message the service sends to a topic another party defines: a "send"
//     operation on a channel carrying the "request" message, with a reply channel when the send
//     expects one.
//
// Nothing is declared twice: events and sends are the service's event and send declarations
// (contract.RegisterEvent, contract.RegisterSend), the same ones its Contract Document lists.
//
// Payloads reference the document's named schemas, carried over into the AsyncAPI document's own
// components.schemas under the same names. The Contract Document's schemas are OpenAPI 3.0 Schema
// Objects; AsyncAPI 3.0's default schema format is JSON Schema, so the one construct the two
// disagree on - nullability - is rewritten: {"type":T,"nullable":true} becomes {"type":[T,"null"]},
// and the nullable wrapper {"nullable":true,"allOf":[X]} becomes {"anyOf":[X,{"type":"null"}]}.
package asyncapi

import (
	"strconv"
	"strings"

	"github.com/daniellepelley/benzene-go/contract"
)

// Document is a minimal AsyncAPI 3.0 document: enough of the shape to describe a Benzene service's
// received topics (with their replies), its events and its sends.
type Document struct {
	AsyncAPI           string               `json:"asyncapi"`
	ID                 string               `json:"id,omitempty"`
	DefaultContentType string               `json:"defaultContentType"`
	Info               Info                 `json:"info"`
	Channels           map[string]Channel   `json:"channels"`
	Operations         map[string]Operation `json:"operations"`
	Components         *Components          `json:"components,omitempty"`
}

// Components holds the named payload schemas messages reference.
type Components struct {
	Schemas map[string]map[string]any `json:"schemas"`
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

// Message carries a payload schema (JSON Schema; a $ref into components.schemas for a named type).
type Message struct {
	Name    string         `json:"name,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Operation is what the application does with a channel: receive (an inbound handler) or send (an
// event or a send declaration). An operation whose message has a reply carries a reply referencing the
// reply channel.
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

type config struct {
	title          string
	version        string
	description    string
	responseSuffix string
}

// Option configures Generate.
type Option func(*config)

// WithTitle overrides the document title (default: the Contract Document's service, or
// "benzene-service" when that is empty).
func WithTitle(title string) Option { return func(c *config) { c.title = title } }

// WithVersion overrides the document version (default: the Contract Document's serviceVersion, or
// "0.0.0" when that is empty).
func WithVersion(version string) Option { return func(c *config) { c.version = version } }

// WithDescription overrides the info description (default: the Contract Document's description).
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

// DefaultResponseTopicSuffix is the default reply-channel address suffix (see WithResponseTopicSuffix).
const DefaultResponseTopicSuffix = "response"

// Generate builds an AsyncAPI 3.0 document from doc (typically contract.Derive's result): a receive
// operation per request, a send operation per event and per send, and a reply channel wherever a
// response is declared. benzene: entries are skipped. The result marshals to valid AsyncAPI 3.0 JSON
// with encoding/json; map keys marshal sorted, so the output is deterministic for a given document and
// option set. Generate never mutates doc.
func Generate(doc contract.Document, opts ...Option) *Document {
	cfg := config{title: doc.Service, version: doc.ServiceVersion, description: doc.Description, responseSuffix: DefaultResponseTopicSuffix}
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

	// add records one operation on topic carrying message (named name), with a reply channel when
	// reply is non-nil. Reusing the channel book by address means a topic that is both received and
	// sent becomes one channel carrying both messages, not two channels or a silent overwrite
	// (matching the .NET builder).
	add := func(action, topic, name string, payload, reply map[string]any) {
		key := book.addMessage(topic, name, Message{Name: name, Payload: toJSONSchema(payload)})
		op := Operation{Action: action, Channel: Ref{Ref: channelRef(key)}}
		if reply != nil {
			replyKey := book.addMessage(topic+":"+cfg.responseSuffix, "response", Message{Name: "response", Payload: toJSONSchema(reply)})
			op.Reply = &Reply{Channel: Ref{Ref: channelRef(replyKey)}}
		}
		operations[uniqueOp(action+"_"+operationSlug(topic), usedOps)] = op
	}
	for _, r := range doc.Requests {
		if !contract.IsReserved(r.Topic) {
			add("receive", r.Topic, "request", r.Request, r.Response)
		}
	}
	for _, e := range doc.Events {
		if !contract.IsReserved(e.Topic) {
			add("send", e.Topic, "event", e.Message, nil)
		}
	}
	for _, s := range doc.Sends {
		if !contract.IsReserved(s.Topic) {
			add("send", s.Topic, "request", s.Request, s.Response)
		}
	}

	out := &Document{
		AsyncAPI:           "3.0.0",
		ID:                 buildID(cfg.title),
		DefaultContentType: "application/json",
		Info:               Info{Title: cfg.title, Version: cfg.version, Description: cfg.description},
		Channels:           book.channels,
		Operations:         operations,
	}
	if len(doc.Components.Schemas) > 0 {
		out.Components = &Components{Schemas: make(map[string]map[string]any, len(doc.Components.Schemas))}
		for name, schema := range doc.Components.Schemas {
			out.Components.Schemas[name] = toJSONSchema(schema)
		}
	}
	return out
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

// toJSONSchema deep-copies an OpenAPI 3.0 schema from the Contract Document into JSON Schema, so
// Generate never mutates the document: nullable: true moves into the type ([T, "null"]), and the
// nullable wrapper {"nullable":true,"allOf":[X]} becomes {"anyOf":[X,{"type":"null"}]}. $refs are
// kept - they point at components.schemas, which Generate carries over under the same names. A nil
// schema copies to nil.
func toJSONSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = copyValue(v)
	}
	if out["nullable"] != true {
		return out
	}
	delete(out, "nullable")
	if allOf, ok := out["allOf"].([]any); ok && len(allOf) == 1 {
		delete(out, "allOf")
		delete(out, "type")
		out["anyOf"] = []any{allOf[0], map[string]any{"type": "null"}}
		return out
	}
	if typ, ok := out["type"].(string); ok {
		out["type"] = []string{typ, "null"}
	}
	return out
}

func copyValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return toJSONSchema(typed)
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
// "_2", "_3", ... suffix, in document order, so the document stays valid and deterministic. base is
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
