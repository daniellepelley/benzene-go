// Package awssqs is the inbound half of the AWS SQS binding: a Lambda function triggered by an
// SQS event source mapping. It needs no third-party dependency - AWS delivers the batch as
// plain JSON to the Lambda invocation (the event source mapping itself does the polling and
// SigV4-signed ReceiveMessage/DeleteMessage calls), so this is "just" JSON parsing, the same
// shape as awslambda's own HTTP v2 adapter.
//
// This is transport-bindings.md's AWS Lambda catalog entry: "SQS / SNS / Kafka batches (topic
// from the topic message attribute or envelope; one scope per record)".
package awssqs

import (
	"context"
	"encoding/json"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// sqsEvent mirrors the fields this adapter needs from the Lambda SQS event source mapping's
// payload - see https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html.
type sqsEvent struct {
	Records []sqsRecord `json:"Records"`
}

type sqsRecord struct {
	MessageID         string                         `json:"messageId"`
	Body              string                         `json:"body"`
	MessageAttributes map[string]sqsMessageAttribute `json:"messageAttributes"`
}

type sqsMessageAttribute struct {
	StringValue string `json:"stringValue"`
}

// batchResponse is the Lambda event source mapping's "partial batch failure" report shape -
// requires FunctionResponseTypes: ["ReportBatchItemFailures"] on the event source mapping (see
// examples/aws-sqs-helloworld/template.yaml) - so only the listed message IDs are redelivered,
// not the whole batch.
type batchResponse struct {
	BatchItemFailures []batchItemFailure `json:"batchItemFailures"`
}

type batchItemFailure struct {
	ItemIdentifier string `json:"itemIdentifier"`
}

// Option configures Handler.
type Option func(*options)

type options struct {
	settlement envelope.Settlement
}

// WithSettlement sets how Handler settles a message whose invocation failed (transport-bindings.md §1,
// obligation 8). The default - the zero envelope.Settlement - reports it as a batch item failure, so
// SQS redelivers it after its visibility timeout and the queue's redrive policy eventually
// dead-letters it; envelope.AtMostOnce acknowledges it whatever the outcome, and
// IgnoreUnmatchedTopics acknowledges a message whose topic no handler serves.
func WithSettlement(settlement envelope.Settlement) Option {
	return func(o *options) { o.settlement = settlement }
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda triggered by an SQS event
// source mapping. Each record in the batch gets its own pipeline invocation - its own DI scope - so
// one record's handler can't see another's state. A record the settlement does not acknowledge (by
// default, one whose result is unsuccessful) is reported back as a batch item failure: settlement is
// per message (the "queue" shape of transport-bindings.md §1, obligation 8).
func Handler(builder *benzene.ApplicationBuilder, opts ...Option) awslambda.HandlerFunc {
	settlement := newOptions(opts).settlement
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var sqsEvt sqsEvent
		if err := json.Unmarshal(event, &sqsEvt); err != nil {
			return nil, fmt.Errorf("awssqs: malformed SQS event: %w", err)
		}

		var failures []batchItemFailure
		for _, record := range sqsEvt.Records {
			req := resolveRequest(record, builder.ReservedNames.Topic())
			if outcome := settlement.Dispatch(ctx, builder.Pipeline, builder.Container, req); !outcome.Acknowledge {
				failures = append(failures, batchItemFailure{ItemIdentifier: record.MessageID})
			}
		}

		return json.Marshal(batchResponse{BatchItemFailures: failures})
	}
}

// resolveRequest resolves a record's topic and headers per wire-contracts.md §2: "On transports
// where the envelope isn't used but native attributes exist (SQS/SNS message attributes), the
// topic travels as an attribute named topic" - attributes besides "topic" become headers. When
// no topic attribute is present, the record's Body is instead parsed as a full wire.Request
// envelope (topic/headers/body) - the "or envelope" fallback transport-bindings.md's catalog
// entry describes. If neither yields a topic, the request carries an empty topic, which
// RouterMiddleware maps to ValidationError (see router.go) rather than silently dropping the
// record - it's still reported as a batch item failure, not lost.
func resolveRequest(record sqsRecord, topicKey string) wire.Request {
	metadata := make(map[string]string, len(record.MessageAttributes))
	for name, attr := range record.MessageAttributes {
		metadata[name] = attr.StringValue
	}
	topic, headers := wire.ResolveMetadataTopic(metadata, topicKey)

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: record.Body}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal([]byte(record.Body), &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}

	return wire.Request{Topic: "", Headers: headers, Body: record.Body}
}
