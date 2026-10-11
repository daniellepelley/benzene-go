package wire

import "strings"

// DefaultTopicKey is the reserved native-metadata key the topic travels under on transports
// that carry Benzene metadata natively but do not use the envelope - SQS/SNS message
// attributes, Pub/Sub attributes, Service Bus/Event Hub properties, Kafka/RabbitMQ headers
// (wire-contracts.md §2, tier A). It keeps the envelope field's spelling so one concept has
// one name wherever it travels.
//
// Per wire-contracts.md §2 ("Reserved names are defaults") this name is a default, not a
// literal to hard-code: an implementation MUST let a service replace it with a single
// injectable value, and the override MUST apply to both inbound bindings and outbound clients.
// ResolveMetadataTopic takes the key as a parameter for exactly this reason; a binding passes
// DefaultTopicKey unless the service configured another.
const DefaultTopicKey = "topic"

// DefaultCorrelationKey is the reserved outbound correlation header of wire-contracts.md §2
// (tier C). Like DefaultTopicKey it is a default, not a literal to hard-code - it is
// configurable via ReservedNames.
const DefaultCorrelationKey = "x-correlation-id"

// DefaultVersionKey is the header a message's version travels under (wire-contracts.md §2, tier
// A): the one signal that selects the handler (core-concepts.md §2). There is deliberately no
// fallback list - a producer's own "version" or "x-version" header (an API or client SDK version,
// not a payload schema version) is an ordinary header and selects nothing, because reading it would
// silently route the message to another version's handler (versioning.md §2.1). Like
// DefaultTopicKey it is a default, not a literal to hard-code: a service whose producers already use
// another name sets ReservedNames.VersionKey, which replaces this name rather than joining it.
const DefaultVersionKey = "benzene-version"

// ReservedNames holds the configurable reserved metadata/header names of wire-contracts.md §2
// ("Reserved names are defaults"). The spec requires an implementation to expose them "as a
// single injectable value" a service sets once and applies to both its inbound bindings and its
// outbound clients - an override on only one side would send messages the service cannot itself
// receive. The zero value uses the defaults, and an empty field falls back to its default via
// the accessors, so a service overrides only the name it needs and leaves the rest standard.
//
// The defaults carry interop: two services that have not overridden anything interoperate
// untouched, and a service that renames a key is responsible for agreeing that change with
// whatever it talks to.
type ReservedNames struct {
	// TopicKey is the native-metadata key the topic travels under on queue-shaped transports
	// (SQS/SNS message attributes, Pub/Sub attributes; tier A). Empty means DefaultTopicKey.
	TopicKey string
	// CorrelationKey is the outbound correlation header (tier C). Empty means
	// DefaultCorrelationKey.
	CorrelationKey string
	// VersionKey is the header the message's version travels under (tier A). Empty means
	// DefaultVersionKey. It is one name, not a list: a service whose producers already send the
	// version under another name sets that name here, and DefaultVersionKey then stops being read.
	VersionKey string
}

// Topic returns the configured topic metadata key, or DefaultTopicKey when unset.
func (n ReservedNames) Topic() string {
	if n.TopicKey != "" {
		return n.TopicKey
	}
	return DefaultTopicKey
}

// Correlation returns the configured correlation header name, or DefaultCorrelationKey when unset.
func (n ReservedNames) Correlation() string {
	if n.CorrelationKey != "" {
		return n.CorrelationKey
	}
	return DefaultCorrelationKey
}

// Version returns the configured version header name, or DefaultVersionKey when unset. Pass it to
// ResolveVersion (or benzene.WithVersionKey) so an override made once via UseReservedNames drives
// the inbound read path too.
func (n ReservedNames) Version() string {
	if n.VersionKey != "" {
		return n.VersionKey
	}
	return DefaultVersionKey
}

// ResolveVersion returns the message's version, read from headers under key (wire-contracts.md §2),
// matched case-insensitively (wire-contracts.md preamble). It returns "" when the header is absent
// or empty - "no version signalled", which core-concepts.md §2 sends to the topic's default handler,
// not an error. key is typically ReservedNames.Version(); no other header is consulted.
func ResolveVersion(headers map[string]string, key string) string {
	for name, value := range headers {
		if value != "" && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}

// ResolveMetadataTopic splits a transport's native string->string metadata dictionary into the
// resolved topic and the remaining headers, following wire-contracts.md §2 and
// transport-metadata-cases.json:
//
//   - The value under topicKey becomes the topic. The key match is case-insensitive on read
//     (wire-contracts.md preamble), and that key is stripped from the returned headers.
//   - Every other entry passes through as a header verbatim.
//   - Only topicKey routes: a different key (e.g. an old "benzene-topic") stays an ordinary
//     header and leaves the topic empty, so two services cannot silently drift onto different
//     routing keys.
//   - An absent topicKey leaves the topic empty (not an error): the caller/router decides what
//     an unresolved topic means (RouterMiddleware maps it to validation-error).
//
// The returned headers map is always non-nil (empty if the metadata held only the topic), so a
// caller can add to it without a nil check. Duplicate keys cannot occur in a Go map; a
// transport that allows duplicate native keys resolves last-value-wins before calling this.
func ResolveMetadataTopic(metadata map[string]string, topicKey string) (topic string, headers map[string]string) {
	headers = make(map[string]string, len(metadata))
	for name, value := range metadata {
		if topic == "" && strings.EqualFold(name, topicKey) {
			topic = value
			continue
		}
		headers[name] = value
	}
	return topic, headers
}
