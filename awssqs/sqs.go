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
	"strings"

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

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda triggered by an SQS event
// source mapping. Each record in the batch gets its own pipeline invocation - its own DI scope,
// via envelope.Dispatch - so one record's handler can't see another's state. A record whose
// dispatch result is not a success status is reported back as a batch item failure.
func Handler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var sqsEvt sqsEvent
		if err := json.Unmarshal(event, &sqsEvt); err != nil {
			return nil, fmt.Errorf("awssqs: malformed SQS event: %w", err)
		}

		var failures []batchItemFailure
		for _, record := range sqsEvt.Records {
			req := resolveRequest(record)
			resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, req)
			if !benzene.Status(resp.StatusCode).IsSuccess() {
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
func resolveRequest(record sqsRecord) wire.Request {
	headers := make(map[string]string, len(record.MessageAttributes))
	var topic string
	for name, attr := range record.MessageAttributes {
		if strings.EqualFold(name, "topic") {
			topic = attr.StringValue
			continue
		}
		headers[name] = attr.StringValue
	}

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
