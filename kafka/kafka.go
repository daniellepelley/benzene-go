// Package kafka is the Kafka binding, matching the main repo's `Benzene.Kafka.Core` /
// transport-bindings.md "Kafka (self-hosted consumer)" entry exactly: a consumer loop that
// dispatches each record through a Benzene pipeline, and an outbound client that publishes
// wire messages, applied to a self-hosted (or managed - MSK, Confluent, Event Hubs' Kafka
// surface) broker.
//
// The mapping is a deliberately thin pass-through, unlike the cloud queue bindings
// (awssqs/awssns/gcppubsub), which invent a "topic" attribute/header convention because their
// transports have no native concept of topic at all:
//
//   - Topic: the Kafka topic itself, verbatim (kafkago.Message.Topic on read; Client writes
//     to a Kafka topic named after the Benzene topic). One Kafka topic maps to exactly one
//     Benzene (unversioned) topic - this binding does not multiplex several Benzene topics
//     over one Kafka topic the way SQS/SNS/EventBridge do, because Kafka's own topic already
//     is that routing key (`Benzene.Kafka.Core.KafkaMessage.KafkaMessageTopicGetter` /
//     `KafkaSendMessageTopicGetter` both do exactly `new Topic(topic)` - no header, no
//     envelope).
//   - Headers: every Kafka header, verbatim, both directions (UTF-8 decoded on read) - no
//     reserved header name, no fallback parsing.
//   - Body: the raw message value, verbatim, both directions - no envelope wrapping.
//
// This module depends on github.com/segmentio/kafka-go - a broker wire protocol is not
// reasonably hand-rollable, unlike the cloud bindings' HTTP/JSON contracts - which is why it
// lives in its own Go module (see RELEASING.md): the root module stays zero-dependency.
// Both halves depend only on narrow interfaces (MessageSource, MessageWriter) that
// *kafka.Reader and *kafka.Writer satisfy as-is, so this package's own tests run against
// fakes, no live broker needed.
//
// Failure semantics: the spec describes "no response channel — result mapping is
// acknowledge/log only", and Kafka has no broker-side per-message redelivery or dead-letter
// queue - unlike SQS (batchItemFailures), SNS (async-invoke retry), or Pub/Sub (nack) there
// is no platform machinery to hand a failed message to; there is only the consumer group's
// offset. Consumer therefore commits every dispatched message - success or not - and reports
// each non-success dispatch to the OnFailure hook first (this port's log/dead-letter
// extension point, playing the reference implementation's ILogger role explicitly rather
// than folding it into the framework). Not committing would be the only alternative, and
// that replays the same poison message forever, stalling the partition. Transport-level
// failures (fetch, commit) are different: they return from Run, offsets uncommitted, so a
// restarted consumer resumes with at-least-once delivery.
package kafka

import (
	"context"
	"errors"
	"fmt"

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

// Consumer runs a Benzene pipeline over a Kafka consumer group. Each fetched message gets its
// own pipeline invocation - its own DI scope, via envelope.Dispatch - and is committed after
// dispatch (see the package doc for why failures commit too).
type Consumer struct {
	Source  MessageSource
	Builder *benzene.ApplicationBuilder

	// OnFailure, when non-nil, is called for each message whose dispatch result is not a
	// success status, before that message's offset is committed. This is where a dead-letter
	// publish or failure log belongs. A nil OnFailure means non-success results are simply
	// committed past - configure one unless the pipeline's own middleware already records
	// failures.
	OnFailure func(ctx context.Context, msg kafkago.Message, resp wire.Response)
}

// Run fetches, dispatches, and commits messages until ctx is cancelled (returns nil - the
// clean shutdown) or the source fails (returns the fetch/commit error, offsets of any
// undispatched messages uncommitted, so a restarted consumer resumes at-least-once). Note
// this is the loop's contract with its *owner*, not a per-message error channel - a message
// that fails to dispatch never stops the loop (see the package doc).
func (c *Consumer) Run(ctx context.Context) error {
	for {
		msg, err := c.Source.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("kafka: fetch failed: %w", err)
		}

		resp := envelope.Dispatch(ctx, c.Builder.Pipeline, c.Builder.Container, resolveRequest(msg))
		if !benzene.Status(resp.StatusCode).IsSuccess() && c.OnFailure != nil {
			c.OnFailure(ctx, msg, resp)
		}

		if err := c.Source.CommitMessages(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("kafka: commit failed: %w", err)
		}
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

// resolveRequest maps a fetched record onto a wire.Request per the package doc: the Kafka
// topic becomes the Benzene topic verbatim, every Kafka header becomes a wire header verbatim
// (UTF-8 decoded; on duplicate keys, matching wire-contracts.md §2, the last value wins - the
// natural effect of this map assignment loop), and the message value becomes the body
// verbatim. No reserved header name, no envelope-in-value fallback - Kafka's own topic
// already is the routing key, so there is nothing to lift out of the payload.
func resolveRequest(msg kafkago.Message) wire.Request {
	headers := make(map[string]string, len(msg.Headers))
	for _, header := range msg.Headers {
		headers[header.Key] = string(header.Value)
	}
	return wire.Request{Topic: msg.Topic, Headers: headers, Body: string(msg.Value)}
}
