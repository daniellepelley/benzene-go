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

// DefaultVersionKeys is the default ordered fallback list of header names the inbound payload
// schema version is read from (wire-contracts.md §2 tier C, versioning.md §2.1): the first of
// these present in a message's headers wins, and version is always written back as the first
// name, "benzene-version". "benzene-version" is the unambiguous, collision-free default;
// "version"/"x-version" are recognised only because plenty of producers already emit one for
// their own (unrelated) purposes, so the list lets a service opt into reading those without
// forcing a producer to rename a header first.
//
// Because that is also how it can go wrong - a pre-existing "version" header meaning something
// else read as the payload schema version - versioning.md §2.1 requires the list be
// configurable: a service with its own conflicting use narrows it to just "benzene-version"
// (or replaces it wholesale) via ReservedNames.VersionKeys. Treat this value as read-only; to
// change the list, set ReservedNames.VersionKeys rather than mutating the slice.
var DefaultVersionKeys = []string{"benzene-version", "version", "x-version"}

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
	// VersionKeys is the ordered fallback list of header names the inbound payload schema
	// version is read from (tier C), first-present-wins - versioning.md §2.1. Nil/empty means
	// DefaultVersionKeys. Narrow it to just "benzene-version", reorder it, or replace it
	// wholesale when a producer already emits a "version"/"x-version" header meaning something
	// else. Like the topic key, this is an application-wide reserved-name override a service
	// sets once and applies to both its inbound read path and its outbound writes.
	VersionKeys []string
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

// Version returns the configured ordered version-header fallback list, or DefaultVersionKeys
// when unset. Pass the result to ResolveVersion (or benzene.WithVersionKeys) so an override
// made once via UseReservedNames drives the inbound read path too.
func (n ReservedNames) Version() []string {
	if len(n.VersionKeys) > 0 {
		return n.VersionKeys
	}
	return DefaultVersionKeys
}

// ResolveVersion returns the payload schema version read from headers using the ordered
// fallback list keys (versioning.md §2.1): the first key present with a non-empty value wins,
// matched case-insensitively (wire-contracts.md preamble). It returns "" when no listed key is
// present with a value - "no version signalled", which versioning.md §2.2 treats as the topic's
// default version, not an error. A present-but-empty header is treated as absent, so the
// fallback continues to the next name. keys is typically ReservedNames.Version().
func ResolveVersion(headers map[string]string, keys []string) string {
	for _, key := range keys {
		for name, value := range headers {
			if value != "" && strings.EqualFold(name, key) {
				return value
			}
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
