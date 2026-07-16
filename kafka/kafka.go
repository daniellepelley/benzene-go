// Package kafka is the Kafka binding: a consumer loop that dispatches each record through a
// Benzene pipeline, and an outbound client that publishes wire messages. It is
// transport-bindings.md's "SQS / SNS / Kafka batches (topic from the topic message attribute
// or envelope; one scope per record)" catalog entry, applied to a self-hosted (or managed -
// MSK, Confluent, Event Hubs' Kafka surface) broker.
//
// This module depends on github.com/segmentio/kafka-go - a broker wire protocol is not
// reasonably hand-rollable, unlike the cloud bindings' HTTP/JSON contracts - which is why it
// lives in its own Go module (see RELEASING.md): the root module stays zero-dependency.
// Both halves depend only on narrow interfaces (MessageSource, MessageWriter) that
// *kafka.Reader and *kafka.Writer satisfy as-is, so this package's own tests run against
// fakes, no live broker needed.
//
// Terminology note: a *Kafka* topic is the stream a Reader/Writer is configured with; the
// *Benzene* topic is the per-message routing key, travelling as a message header named
// "topic" (wire-contracts.md §2), exactly as it travels as a message attribute on SQS/SNS.
// One Kafka topic can therefore carry many Benzene topics.
//
// Failure semantics: Kafka has no broker-side per-message redelivery or dead-letter queue -
// unlike SQS (batchItemFailures), SNS (async-invoke retry), or Pub/Sub (nack) there is no
// platform machinery to hand a failed message to; there is only the consumer group's offset.
// Consumer therefore commits every dispatched message - success or not - and reports each
// non-success dispatch to the OnFailure hook first, where the application decides what a
// failure means (publish to its own dead-letter Kafka topic via Client, log and move on).
// Not committing would be the only alternative, and that replays the same poison message
// forever, stalling the partition. Transport-level failures (fetch, commit) are different:
// they return from Run, offsets uncommitted, so a restarted consumer resumes with
// at-least-once delivery.
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// resolveRequest resolves a message's topic and headers per wire-contracts.md §2, the same
// order as awssqs/awssns/gcppubsub: the "topic" message header (remaining headers become wire
// headers; on duplicates the last value wins), else the message value parsed as a full
// wire.Request envelope, else an empty topic, which RouterMiddleware maps to ValidationError -
// the message is reported to OnFailure, never silently dropped.
func resolveRequest(msg kafkago.Message) wire.Request {
	headers := make(map[string]string, len(msg.Headers))
	var topic string
	for _, header := range msg.Headers {
		if strings.EqualFold(header.Key, "topic") {
			topic = string(header.Value)
			continue
		}
		headers[header.Key] = string(header.Value)
	}

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: string(msg.Value)}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal(msg.Value, &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}

	return wire.Request{Topic: "", Headers: headers, Body: string(msg.Value)}
}
