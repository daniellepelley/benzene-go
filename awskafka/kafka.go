// Package awskafka is the AWS Lambda Kafka inbound binding: a Lambda function triggered by an
// Amazon MSK or self-managed-Kafka event source mapping. It is zero-dependency - AWS delivers the
// records as plain JSON to the Lambda invocation (the value/key base64-encoded, header values as
// byte arrays) - so this is "just" JSON parsing plus a base64 decode, and there is no outbound side
// (producing to Kafka is the publish; the trigger is read-only), so no AWS SDK is needed and it
// lives in the root module.
//
// It is DISTINCT from this repo's self-hosted `kafka` module: that one runs its own consumer loop
// against a broker (needing segmentio/kafka-go); this one is a zero-dependency Lambda adapter for
// AWS's managed event source mapping. Both map one Kafka topic to one Benzene topic and pass Kafka
// headers through verbatim, matching Benzene.Kafka.Core / Benzene.Aws.Lambda.Kafka.
//
//   - Event: the records are grouped by "topic-partition" key (e.g. "orders-0"), each group a list
//     of records ordered by offset.
//   - Topic: the record's Kafka topic (one Kafka topic = one Benzene topic, verbatim - no envelope).
//   - Body: the record's value, base64-decoded into the raw bytes the producer wrote (typically JSON).
//   - Headers: the Kafka headers, passed through verbatim (their byte-array values UTF-8 decoded),
//     matching the self-hosted binding.
//   - Batching: records within a partition are ordered, so each partition is processed sequentially
//     and STOPS at its first failure, reporting {partition, offset} - the offset AWS resumes that
//     partition from. Partitions are independent (a failure in one does not stop another). This is
//     the object-shaped batchItemFailures the Kafka/MSK mapping reads (unlike the string
//     itemIdentifier of SQS/Kinesis/DynamoDB), requiring FunctionResponseTypes:
//     ["ReportBatchItemFailures"] on the mapping.
package awskafka

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// kafkaEvent is the Lambda MSK/Kafka event source mapping's payload: records grouped by
// "topic-partition" key.
type kafkaEvent struct {
	EventSource string                   `json:"eventSource"`
	Records     map[string][]kafkaRecord `json:"records"`
}

type kafkaRecord struct {
	Topic     string `json:"topic"`
	Partition int    `json:"partition"`
	Offset    int64  `json:"offset"`
	Timestamp int64  `json:"timestamp"`
	Key       string `json:"key"`   // base64
	Value     string `json:"value"` // base64
	// Headers is a list of single-entry maps of header name to a byte array (delivered as a JSON
	// array of integers, not base64 - unlike the record value).
	Headers []map[string][]int `json:"headers"`
}

// batchResponse is the Kafka/MSK event source mapping's partial-batch-failure report. Its
// itemIdentifier is an OBJECT {partition, offset} - the Kafka shape - not the string identifier the
// SQS/Kinesis/DynamoDB mappings use.
type batchResponse struct {
	BatchItemFailures []batchItemFailure `json:"batchItemFailures"`
}

type batchItemFailure struct {
	ItemIdentifier itemIdentifier `json:"itemIdentifier"`
}

type itemIdentifier struct {
	Partition string `json:"partition"`
	Offset    int64  `json:"offset"`
}

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda triggered by an MSK/Kafka event
// source mapping. Each record gets its own pipeline invocation (its own DI scope, via
// envelope.Dispatch). Within a topic-partition, records are processed in offset order and processing
// stops at the first unsuccessful one, reporting {partition, offset} so AWS resumes that partition
// from there; every partition is processed (a failure in one does not stop the others). A malformed
// outer event is the one thing returned as a Go error - the sanctioned Lambda Runtime API error
// shape - so the whole batch is retried.
func Handler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var evt kafkaEvent
		if err := json.Unmarshal(event, &evt); err != nil {
			return nil, fmt.Errorf("awskafka: malformed Kafka event: %w", err)
		}

		failures := []batchItemFailure{}
		for partitionKey, records := range evt.Records {
			sort.SliceStable(records, func(i, j int) bool { return records[i].Offset < records[j].Offset })
			for _, record := range records {
				req, err := resolveRequest(record)
				if err != nil {
					// A record whose value cannot be base64-decoded is a poison record: stop this
					// partition and report its offset for redelivery rather than skipping past it.
					failures = append(failures, failAt(partitionKey, record.Offset))
					break
				}
				if _, successful := envelope.DispatchResult(ctx, builder.Pipeline, builder.Container, req); !successful {
					failures = append(failures, failAt(partitionKey, record.Offset))
					break
				}
			}
		}

		return json.Marshal(batchResponse{BatchItemFailures: failures})
	}
}

// failAt builds the partial batch failure for a topic-partition's resume offset.
func failAt(partitionKey string, offset int64) batchItemFailure {
	return batchItemFailure{ItemIdentifier: itemIdentifier{Partition: partitionKey, Offset: offset}}
}

// resolveRequest derives the wire.Request for one Kafka record: the topic verbatim, the
// base64-decoded value as the body, and the Kafka headers passed through verbatim.
func resolveRequest(record kafkaRecord) (wire.Request, error) {
	value, err := base64.StdEncoding.DecodeString(record.Value)
	if err != nil {
		return wire.Request{}, err
	}
	return wire.Request{Topic: record.Topic, Headers: recordHeaders(record), Body: string(value)}, nil
}

// recordHeaders decodes the record's Kafka headers into a flat string map, their byte-array values
// interpreted as UTF-8. A later duplicate header name wins, matching a plain map assignment.
func recordHeaders(record kafkaRecord) map[string]string {
	headers := map[string]string{}
	for _, headerMap := range record.Headers {
		for name, value := range headerMap {
			headers[name] = string(bytesFromInts(value))
		}
	}
	return headers
}

// bytesFromInts converts the JSON integer array a Kafka header value arrives as into bytes.
func bytesFromInts(values []int) []byte {
	out := make([]byte, len(values))
	for i, v := range values {
		out[i] = byte(v)
	}
	return out
}
