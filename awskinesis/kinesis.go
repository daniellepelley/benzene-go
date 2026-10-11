// Package awskinesis is the Kinesis Data Streams inbound binding: a Lambda function triggered by a
// Kinesis stream event source mapping. It is zero-dependency - AWS delivers the stream records as
// plain JSON to the Lambda invocation (the record data base64-encoded inside), so this is "just"
// JSON parsing plus a base64 decode (like awslambda's own adapters and the sibling awsdynamodb
// binding), and there is no outbound side (writing to the stream is the publish; the trigger is
// read-only), so no AWS SDK is needed and it lives in the root module.
//
// It is the direct sibling of awsdynamodb, and matches Benzene.Aws.Lambda.Kinesis:
//   - Topic: the stream name, parsed from the record's stream ARN. Unlike a DynamoDB stream record
//     (which carries an INSERT/MODIFY/REMOVE change type that makes a natural "{table}:{event}"
//     topic), a Kinesis record is just bytes on a stream, so the stream itself is the routing key.
//     Falls back to the bare event name when the ARN carries no stream name. Note the blast radius:
//     because the whole stream maps to one topic, a single mismatched Register name makes every
//     record not-found (a failure) and, with the stop-at-first-failure contract below, stalls the
//     shard at record 0 until the records age out (Kinesis has no built-in dead-letter queue) - so
//     the registered topic must match the stream name exactly.
//   - Body: the record's data, base64-decoded from the wire form Lambda delivers into the raw bytes
//     the producer wrote (typically JSON), so handlers deserialize ordinary structs.
//   - Headers: envelope metadata under kinesis-prefixed keys (partition key, sequence number, ...).
//     There is no benzene-headers convention - a Kinesis record is opaque producer bytes, not a
//     Benzene envelope.
//   - Batching: records are ordered within a shard, so they are processed sequentially and
//     processing STOPS at the first failure, whose SequenceNumber is reported as the partial batch
//     failure - AWS resumes the whole batch from that sequence number onward (it only ever reads
//     the first reported failure for a Kinesis mapping, exactly like DynamoDB Streams). Deliberately
//     not awssqs's concurrent fan-out. This is the "stream" settlement shape (transport-bindings.md
//     §1, obligation 8) defaulting to redeliver; WithSettlement offers the at-most-once
//     (skip-and-continue) override - the way out of the stalled-shard trap above - and
//     unmatched-topic-as-ignored.
package awskinesis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// kinesisEvent is the Lambda Kinesis stream event source mapping's payload.
type kinesisEvent struct {
	Records []kinesisStreamRecord `json:"Records"`
}

type kinesisStreamRecord struct {
	EventID        string             `json:"eventID"`
	EventName      string             `json:"eventName"`
	EventSource    string             `json:"eventSource"`
	EventSourceArn string             `json:"eventSourceARN"`
	AwsRegion      string             `json:"awsRegion"`
	Kinesis        *kinesisRecordData `json:"kinesis"`
}

// kinesisRecordData is the record's kinesis object. Data is the record payload, base64-encoded as
// Lambda delivers it.
type kinesisRecordData struct {
	PartitionKey                string  `json:"partitionKey"`
	SequenceNumber              string  `json:"sequenceNumber"`
	Data                        string  `json:"data"`
	ApproximateArrivalTimestamp float64 `json:"approximateArrivalTimestamp"`
}

// batchResponse is the Lambda event source mapping's partial-batch-failure report, requiring
// FunctionResponseTypes: ["ReportBatchItemFailures"] on the mapping. AWS resumes the shard from the
// reported sequence number.
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

// WithSettlement sets how Handler settles a record whose invocation failed (transport-bindings.md §1,
// obligation 8). The default - the zero envelope.Settlement - stops at the first failed record and
// reports it, so Lambda redelivers the shard from there; envelope.AtMostOnce acknowledges a failed
// record (and a poison record that cannot be decoded) and carries on - skip-and-continue - and
// IgnoreUnmatchedTopics acknowledges a record whose topic no handler serves.
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

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda triggered by a Kinesis stream
// event source mapping. Each record gets its own pipeline invocation - its own DI scope. Records are
// processed in order and processing stops at the first one the settlement does not acknowledge (by
// default, the first unsuccessful one), reporting that record's SequenceNumber as the sole batch
// item failure so AWS resumes the batch from there without silently skipping it - an ordered stream
// must not be checkpointed past a record that did not succeed. The unit of settlement is the shard
// position: one failure redelivers that record and every record after it in the shard. A malformed
// outer event is the one thing returned as a Go error - the sanctioned Lambda Runtime API error
// shape - so the whole batch is retried.
func Handler(builder *benzene.ApplicationBuilder, opts ...Option) awslambda.HandlerFunc {
	settlement := newOptions(opts).settlement
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var evt kinesisEvent
		if err := json.Unmarshal(event, &evt); err != nil {
			return nil, fmt.Errorf("awskinesis: malformed Kinesis stream event: %w", err)
		}

		for _, record := range evt.Records {
			req, err := resolveRequest(record)
			if err != nil {
				// A record whose data cannot be base64-decoded is a poison record: stop here and
				// report it for redelivery rather than checkpointing past it - unless the service
				// chose at-most-once.
				if settlement.Delivery == envelope.AtMostOnce {
					continue
				}
				return failAt(record), nil
			}
			if outcome := settlement.Dispatch(ctx, builder.Pipeline, builder.Container, req); !outcome.Acknowledge {
				return failAt(record), nil
			}
		}

		return json.Marshal(batchResponse{BatchItemFailures: []batchItemFailure{}})
	}
}

// failAt reports record as the single partial batch failure, keyed by its shard sequence number
// (falling back to the event id when a record carries no kinesis object).
func failAt(record kinesisStreamRecord) json.RawMessage {
	identifier := record.EventID
	if record.Kinesis != nil && record.Kinesis.SequenceNumber != "" {
		identifier = record.Kinesis.SequenceNumber
	}
	data, _ := json.Marshal(batchResponse{BatchItemFailures: []batchItemFailure{{ItemIdentifier: identifier}}})
	return data
}

// resolveRequest derives the wire.Request for one stream record: the stream-name topic, the
// base64-decoded data body, and the kinesis-prefixed metadata headers.
func resolveRequest(record kinesisStreamRecord) (wire.Request, error) {
	stream := streamName(record.EventSourceArn)
	topic := record.EventName
	if stream != "" {
		topic = stream
	}

	body := ""
	if record.Kinesis != nil && record.Kinesis.Data != "" {
		decoded, err := base64.StdEncoding.DecodeString(record.Kinesis.Data)
		if err != nil {
			return wire.Request{}, err
		}
		body = string(decoded)
	}

	return wire.Request{Topic: topic, Headers: recordHeaders(record, stream), Body: body}, nil
}

// recordHeaders exposes the stream record's envelope metadata as kinesis-prefixed headers, omitting
// any that are empty.
func recordHeaders(record kinesisStreamRecord, stream string) map[string]string {
	headers := map[string]string{}
	add := func(key, value string) {
		if value != "" {
			headers[key] = value
		}
	}
	add("kinesis-event-id", record.EventID)
	add("kinesis-event-name", record.EventName)
	add("kinesis-stream", stream)
	if record.Kinesis != nil {
		add("kinesis-partition-key", record.Kinesis.PartitionKey)
		add("kinesis-sequence-number", record.Kinesis.SequenceNumber)
		if record.Kinesis.ApproximateArrivalTimestamp != 0 {
			add("kinesis-approximate-arrival-timestamp", strconv.FormatFloat(record.Kinesis.ApproximateArrivalTimestamp, 'f', -1, 64))
		}
	}
	add("kinesis-event-source-arn", record.EventSourceArn)
	add("kinesis-aws-region", record.AwsRegion)
	return headers
}

// streamName parses the stream name out of a Kinesis stream ARN
// (arn:aws:kinesis:region:account:stream/Name), or returns "" when the ARN is missing or not in
// that shape. Split on ':' with a max of 6 so the resource segment stays intact, then on '/'.
func streamName(eventSourceArn string) string {
	if eventSourceArn == "" {
		return ""
	}
	arnParts := strings.SplitN(eventSourceArn, ":", 6)
	if len(arnParts) < 6 {
		return ""
	}
	resourceParts := strings.SplitN(arnParts[5], "/", 2)
	if len(resourceParts) < 2 || resourceParts[0] != "stream" || resourceParts[1] == "" {
		return ""
	}
	return resourceParts[1]
}
