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
