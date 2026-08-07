// Package awsdynamodb is the DynamoDB Streams inbound binding: a Lambda function triggered by a
// DynamoDB stream event source mapping. It is zero-dependency - AWS delivers the change records as
// plain JSON to the Lambda invocation, so this is "just" JSON parsing (like awslambda's own
// adapters), and there is no outbound side (writing to the table is the publish operation; the
// stream is read-only), so no AWS SDK is needed and it lives in the root module rather than its
// own like awssqs/awssns.
//
// This is transport-bindings.md's "DynamoDB Streams" entry:
//   - Topic: "{tableName}:{eventName}" - the table parsed from the stream ARN plus the change type
//     (INSERT/MODIFY/REMOVE); the two things that identify a CDC event. Falls back to the bare
//     event name when the ARN carries no table name.
//   - Body: the record's image unmarshalled from DynamoDB AttributeValue format into plain JSON
//     (NewImage, else OldImage, else Keys - the most complete state available), so handlers
//     deserialize ordinary structs; empty when the record carries no image.
//   - Headers: envelope metadata under dynamodb-prefixed keys. There is no _benzeneHeaders
//     convention - these events originate from table writes, not a Benzene publisher, so there is
//     no producer side to embed wire headers.
//   - Batching: records are ordered CDC within a shard, so they are processed sequentially and
//     processing STOPS at the first failure, whose SequenceNumber is reported as the partial batch
//     failure - Lambda checkpoints there and redelivers the rest. Deliberately not awssqs's
//     concurrent fan-out.
package awsdynamodb

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

// dynamoDBEvent is the Lambda DynamoDB stream event source mapping's payload. The top-level record
// fields are camelCase while the nested dynamodb object's fields are PascalCase - matching AWS's
// actual shape, not a normalization.
type dynamoDBEvent struct {
	Records []dynamoDBStreamRecord `json:"Records"`
}

type dynamoDBStreamRecord struct {
	EventID        string              `json:"eventID"`
	EventName      string              `json:"eventName"`
	EventSource    string              `json:"eventSource"`
	EventSourceArn string              `json:"eventSourceARN"`
	AwsRegion      string              `json:"awsRegion"`
	Dynamodb       *dynamoDBStreamData `json:"dynamodb"`
}

// dynamoDBStreamData is the record's dynamodb object. Keys/NewImage/OldImage are kept raw, in
// AttributeValue format, and unmarshalled to plain JSON by attributeMapToJSON only for the one
// image that becomes the body.
type dynamoDBStreamData struct {
	Keys           json.RawMessage `json:"Keys"`
	NewImage       json.RawMessage `json:"NewImage"`
	OldImage       json.RawMessage `json:"OldImage"`
	SequenceNumber string          `json:"SequenceNumber"`
	StreamViewType string          `json:"StreamViewType"`
}

// batchResponse is the Lambda event source mapping's partial-batch-failure report, requiring
// FunctionResponseTypes: ["ReportBatchItemFailures"] on the mapping. Lambda checkpoints the shard
// at the lowest reported sequence number and redelivers from there.
type batchResponse struct {
	BatchItemFailures []batchItemFailure `json:"batchItemFailures"`
}

type batchItemFailure struct {
	ItemIdentifier string `json:"itemIdentifier"`
}

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda triggered by a DynamoDB stream
// event source mapping. Each record gets its own pipeline invocation - its own DI scope, via
// envelope.Dispatch. Records are processed in order and processing stops at the first unsuccessful
// one, reporting that record's SequenceNumber as the sole batch item failure so Lambda redelivers
// from there without silently skipping it (an ordered CDC stream must not be checkpointed past a
// record that did not succeed). A malformed outer event is the one thing returned as a Go error -
// the sanctioned Lambda Runtime API error shape - so the whole batch is retried.
func Handler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var evt dynamoDBEvent
		if err := json.Unmarshal(event, &evt); err != nil {
			return nil, fmt.Errorf("awsdynamodb: malformed DynamoDB stream event: %w", err)
		}

		for _, record := range evt.Records {
			req, err := resolveRequest(record)
			if err != nil {
				// A record whose image cannot be unmarshalled is a poison record: stop here and
				// report it for redelivery rather than checkpointing past it.
				return failAt(record), nil
			}
			if _, successful := envelope.DispatchResult(ctx, builder.Pipeline, builder.Container, req); !successful {
				return failAt(record), nil
			}
		}

		return json.Marshal(batchResponse{BatchItemFailures: []batchItemFailure{}})
	}
}

// failAt reports record as the single partial batch failure, keyed by its shard sequence number
// (falling back to the event id when a record carries no dynamodb object).
func failAt(record dynamoDBStreamRecord) json.RawMessage {
	identifier := record.EventID
	if record.Dynamodb != nil && record.Dynamodb.SequenceNumber != "" {
		identifier = record.Dynamodb.SequenceNumber
	}
	data, _ := json.Marshal(batchResponse{BatchItemFailures: []batchItemFailure{{ItemIdentifier: identifier}}})
	return data
}

// resolveRequest derives the wire.Request for one stream record: the "{tableName}:{eventName}"
// topic, the plain-JSON body from the record's image, and the dynamodb-prefixed metadata headers.
func resolveRequest(record dynamoDBStreamRecord) (wire.Request, error) {
	table := tableName(record.EventSourceArn)
	topic := record.EventName
	if table != "" {
		topic = table + ":" + record.EventName
	}

	body := ""
	if record.Dynamodb != nil {
		if image := firstImage(record.Dynamodb); image != nil {
			converted, err := attributeMapToJSON(image)
			if err != nil {
				return wire.Request{}, err
			}
			body = string(converted)
		}
	}

	return wire.Request{Topic: topic, Headers: recordHeaders(record, table), Body: body}, nil
}

// firstImage returns the most complete state available: NewImage, else OldImage (REMOVE events),
// else Keys (KEYS_ONLY stream views), or nil when the record carries no image object at all.
func firstImage(data *dynamoDBStreamData) json.RawMessage {
	for _, image := range []json.RawMessage{data.NewImage, data.OldImage, data.Keys} {
		if isJSONObject(image) {
			return image
		}
	}
	return nil
}

// recordHeaders exposes the stream record's envelope metadata as dynamodb-prefixed headers,
// omitting any that are empty.
func recordHeaders(record dynamoDBStreamRecord, table string) map[string]string {
	headers := map[string]string{}
	add := func(key, value string) {
		if value != "" {
			headers[key] = value
		}
	}
	add("dynamodb-event-name", record.EventName)
	add("dynamodb-event-id", record.EventID)
	add("dynamodb-table", table)
	if record.Dynamodb != nil {
		add("dynamodb-sequence-number", record.Dynamodb.SequenceNumber)
		add("dynamodb-stream-view-type", record.Dynamodb.StreamViewType)
	}
	add("dynamodb-event-source-arn", record.EventSourceArn)
	add("dynamodb-aws-region", record.AwsRegion)
	return headers
}

// tableName parses the table name out of a stream ARN
// (arn:aws:dynamodb:region:account:table/Name/stream/timestamp), or returns "" when the ARN is
// missing or not in that shape. The resource segment contains colons of its own (the stream
// timestamp), so the ARN is split on ':' with a max of 6 before the resource is split on '/'.
func tableName(eventSourceArn string) string {
	if eventSourceArn == "" {
		return ""
	}
	arnParts := strings.SplitN(eventSourceArn, ":", 6)
	if len(arnParts) < 6 {
		return ""
	}
	resourceParts := strings.Split(arnParts[5], "/")
	if len(resourceParts) < 2 || resourceParts[0] != "table" {
		return ""
	}
	return resourceParts[1]
}
