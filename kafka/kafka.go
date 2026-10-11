// Package kafka is the Kafka binding, matching the main repo's `Benzene.Kafka.Core` /
// transport-bindings.md "Kafka (self-hosted consumer)" entry exactly: a consumer loop that
// dispatches each record through a Benzene pipeline, and an outbound client that publishes
// wire messages, applied to a self-hosted (or managed - MSK, Confluent, Event Hubs' Kafka
// surface) broker.
//
// The mapping follows transport-bindings.md's Kafka entry:
//
//   - Topic: the record's reserved topic header (default "topic", the same convention as
//     SQS/SNS/Service Bus/RabbitMQ; configurable via ReservedNames, matched case-insensitively),
//     falling back to the Kafka topic the record was consumed from - so one Kafka topic can carry
//     several Benzene topics, and a producer that writes no topic header (a non-Benzene one, or one
//     that names its Kafka topics after Benzene topics, as this module's Client and the .NET and
//     TypeScript clients do) still routes. The same rule as RabbitMQ's routing-key fallback.
//   - Headers: every other Kafka header, verbatim, both directions (UTF-8 decoded on read); the
//     consumed topic header is not repeated as a header.
//   - Body: the raw message value, verbatim, both directions - no envelope wrapping.
//
// This module depends on github.com/segmentio/kafka-go - a broker wire protocol is not
// reasonably hand-rollable, unlike the cloud bindings' HTTP/JSON contracts - which is why it
// lives in its own Go module (see RELEASING.md): the root module stays zero-dependency.
// Both halves depend only on narrow interfaces (MessageSource, MessageWriter) that
// *kafka.Reader and *kafka.Writer satisfy as-is, so this package's own tests run against
// fakes, no live broker needed.
//
// Settlement (transport-bindings.md §1, obligation 8): Kafka is a partition-ordered stream with no
// broker-side per-message redelivery or dead-letter queue - unlike SQS (batchItemFailures), SNS
// (async-invoke retry) or Pub/Sub (nack) there is no platform machinery to hand a failed message
// to; there is only the consumer group's offset, which is positional, so committing a later message
// commits past an earlier one. That leaves two honest choices, and Consumer.Settlement picks one:
//
//   - Skip-and-continue - the default (DefaultDelivery, envelope.AtMostOnce), matching
//     Benzene.Kafka.Core's default: every dispatched message is committed, success or not, after
//     the OnFailure hook has seen a failure (this port's log/dead-letter extension point, playing
//     the reference implementation's ILogger role explicitly). A failed message is NOT redelivered,
//     so the partition keeps moving; wire OnFailure to a dead-letter publish if a failure must not
//     be lost. The spec allows this default for a stream binding, and this is the documentation
//     it requires.
//   - At-least-once - envelope.AtLeastOnce: a failed message is left uncommitted and re-dispatched
//     in place after ErrorBackoff until it succeeds, so it is never skipped; the partition waits
//     behind it (a poison message stalls it until the handler or the data is fixed). Matches
//     Benzene.Kafka.Core's CommitOnlyOnSuccess = true.
//
// Either way the unit of settlement is one message in partition order, committed (or not) before
// the next is fetched, and IgnoreUnmatchedTopics commits past a message whose topic no handler
// serves. Transport-level failures (fetch, commit) are different: they return from Run, offsets
// uncommitted, so a restarted consumer resumes with at-least-once delivery.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"
)

// MessageSource is the narrow slice of *kafka.Reader Consumer depends on. Depending on this
// interface, rather than the concrete reader, makes Consumer testable with a fake - no live
// broker needed. A *kafka.Reader constructed with a GroupID satisfies it as-is (FetchMessage
// + CommitMessages is kafka-go's explicit-commit mode; Reader.ReadMessage would auto-commit
// before the pipeline has run).
type MessageSource interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// DefaultDelivery is what a zero Consumer.Settlement does with a failed message: skip-and-continue
// (commit past it), the default the spec allows a stream binding - see the package doc.
const DefaultDelivery = envelope.AtMostOnce

// Consumer runs a Benzene pipeline over a Kafka consumer group. Each fetched message gets its
// own pipeline invocation - its own DI scope - and is committed once its Settlement acknowledges it
// (see the package doc for the two delivery modes).
type Consumer struct {
	Source  MessageSource
	Builder *benzene.ApplicationBuilder

	// Settlement decides what a failed message does (transport-bindings.md §1, obligation 8). The
	// zero value is DefaultDelivery, skip-and-continue: the message is committed past.
	// envelope.AtLeastOnce instead leaves it uncommitted and re-dispatches it after ErrorBackoff
	// until it succeeds. IgnoreUnmatchedTopics commits past a message whose topic no handler serves
	// even under AtLeastOnce.
	Settlement envelope.Settlement

	// ErrorBackoff is how long an AtLeastOnce Consumer waits before re-dispatching a failed message,
	// so a failing handler does not become a hot loop. Default 1s when 0.
	ErrorBackoff time.Duration

	// OnFailure, when non-nil, is called for each dispatch whose result is not a success - under
	// AtLeastOnce, for every failed attempt - before the message is committed or retried. This is
	// where a dead-letter publish or failure log belongs. A nil OnFailure means a skipped failure
	// leaves no trace - configure one unless the pipeline's own middleware already records failures.
	OnFailure func(ctx context.Context, msg kafkago.Message, resp wire.Response)

	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2) used to read the
	// topic header. Leave it zero to inherit the Builder's reserved names; set it only to differ
	// from the Builder.
	ReservedNames wire.ReservedNames

	// sleep is the backoff primitive, injectable for tests. nil means sleepContext.
	sleep func(ctx context.Context, d time.Duration)
}

// topicKey resolves the reserved topic header key: the Consumer's explicit override if set, else
// the Builder's reserved names (which default to "topic").
func (c *Consumer) topicKey() string {
	if c.ReservedNames.TopicKey != "" {
		return c.ReservedNames.TopicKey
	}
	return c.Builder.ReservedNames.Topic()
}

// Run fetches, dispatches, and commits messages until ctx is cancelled (returns nil - the
// clean shutdown, leaving any message an AtLeastOnce Consumer is still retrying uncommitted) or
// the source fails (returns the fetch/commit error, offsets of any undispatched messages
// uncommitted, so a restarted consumer resumes at-least-once). Note this is the loop's contract with
// its *owner*, not a per-message error channel - a message that fails to dispatch never stops the
// loop (see the package doc).
func (c *Consumer) Run(ctx context.Context) error {
	settlement := c.Settlement
	if settlement.Delivery == envelope.BindingDefault {
		settlement.Delivery = DefaultDelivery
	}
	for {
		msg, err := c.Source.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("kafka: fetch failed: %w", err)
		}

		for {
			outcome := settlement.Dispatch(ctx, c.Builder.Pipeline, c.Builder.Container, resolveRequest(msg, c.topicKey()))
			if !outcome.Successful && c.OnFailure != nil {
				c.OnFailure(ctx, msg, outcome.Response)
			}
			if outcome.Acknowledge {
				break
			}
			// At-least-once: Kafka has no redelivery of its own, so the consumer redelivers in place.
			c.sleepFor(ctx, c.errorBackoff())
			if ctx.Err() != nil {
				return nil
			}
		}

		if err := c.Source.CommitMessages(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("kafka: commit failed: %w", err)
		}
	}
}

func (c *Consumer) errorBackoff() time.Duration {
	if c.ErrorBackoff == 0 {
		return time.Second
	}
	return c.ErrorBackoff
}

func (c *Consumer) sleepFor(ctx context.Context, d time.Duration) {
	if c.sleep != nil {
		c.sleep(ctx, d)
		return
	}
	sleepContext(ctx, d)
}

// sleepContext sleeps for d or until ctx is cancelled, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// errMissingBuilder guards the two required fields with a clear message rather than a nil
// dereference three calls deep.
var errMissingBuilder = errors.New("kafka: Consumer requires both Source and Builder")

// Validate reports whether the Consumer is runnable - call it at startup for a clear error
// instead of a panic from Run's first fetch.
func (c *Consumer) Validate() error {
	if c.Source == nil || c.Builder == nil {
		return errMissingBuilder
	}
	return nil
}

// resolveRequest maps a fetched record onto a wire.Request per the package doc: the topic from the
// reserved topic header (topicKey), else the Kafka topic the record was consumed from; every other
// Kafka header becomes a wire header verbatim (UTF-8 decoded; on duplicate keys, matching
// wire-contracts.md §2, the last value wins - the natural effect of this map assignment loop), and
// the message value becomes the body verbatim. No envelope-in-value fallback.
func resolveRequest(msg kafkago.Message, topicKey string) wire.Request {
	metadata := make(map[string]string, len(msg.Headers))
	for _, header := range msg.Headers {
		metadata[header.Key] = string(header.Value)
	}
	topic, headers := wire.ResolveMetadataTopic(metadata, topicKey)
	if topic == "" {
		topic = msg.Topic
	}
	return wire.Request{Topic: topic, Headers: headers, Body: string(msg.Value)}
}
