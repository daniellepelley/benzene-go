// Package awss3 is the S3 event-notification inbound binding: a Lambda function invoked by S3 when
// an object is created, removed, etc. It is zero-dependency - S3 delivers the notification as plain
// JSON to the Lambda invocation (metadata only; S3 does not include the object's contents), so this
// is "just" JSON parsing (like awslambda's own adapters and the awsdynamodb/awskinesis siblings),
// and there is no outbound side, so no AWS SDK is needed and it lives in the root module.
//
// It matches Benzene.Aws.Lambda.S3, with two deliberate, documented differences:
//   - Topic: "{bucketName}:{eventName}" - the bucket parsed from the record plus the S3 event name
//     (e.g. "my-bucket:ObjectCreated:Put"). The .NET binding routes on the bare event name; this
//     port qualifies it by bucket for consistency with awsdynamodb ("{table}:{event}") and
//     awskinesis (stream-name), since one Lambda may be wired to several buckets. The S3 topic is a
//     local routing concern, not a wire contract (an S3 event is not a Benzene envelope), so this
//     divergence is safe. Falls back to the bare event name when the record carries no bucket.
//   - Body: an s3Notification JSON object - eventName, awsRegion, bucketName, key, size, etag - the
//     object metadata the event carries, so handlers deserialize an ordinary struct. The object key
//     is URL-DECODED here: S3 delivers the key URL-encoded in an event notification (a space as "+",
//     other characters percent-encoded), so a handler that used the raw value with an S3 client
//     would miss any object whose name has a space or special character; the key a handler receives
//     is the real, decoded key.
//   - Headers: s3-prefixed metadata (bucket, decoded key, event name, region, ...).
//   - Failure: an S3-to-Lambda notification is an ASYNCHRONOUS invocation (no batch-item-failure
//     mechanism, unlike the SQS/Kinesis/DynamoDB event source mappings), so a failed record returns
//     a Go error - triggering AWS's own async-invoke retry (2 attempts then the optional
//     destination/DLQ) - exactly the posture awssns takes. This deliberately does NOT mirror the
//     .NET binding's fire-and-forget "mark handled and swallow the failure": silently dropping a
//     failed event would violate this port's no-silent-drop rule. Records are processed in order and
//     processing stops at the first failure; the async retry reprocesses the whole event, so
//     handlers must be idempotent (S3 delivery is at-least-once).
package awss3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// s3Event is the Lambda S3 event-notification payload.
type s3Event struct {
	Records []s3EventRecord `json:"Records"`
}

type s3EventRecord struct {
	EventSource string   `json:"eventSource"`
	EventName   string   `json:"eventName"`
	AwsRegion   string   `json:"awsRegion"`
	S3          s3Entity `json:"s3"`
}

type s3Entity struct {
	Bucket s3Bucket `json:"bucket"`
	Object s3Object `json:"object"`
}

type s3Bucket struct {
	Name string `json:"name"`
}

type s3Object struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
	ETag string `json:"eTag"`
}

// s3Notification is the plain-JSON body a handler deserializes: the object metadata the S3 event
// carries. Mirrors the .NET S3Notification so a handler ported from .NET keeps the same request
// shape.
type s3Notification struct {
	EventName  string `json:"eventName"`
	AwsRegion  string `json:"awsRegion"`
	BucketName string `json:"bucketName"`
	Key        string `json:"key"`
	Size       int64  `json:"size"`
	ETag       string `json:"etag"`
}

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda invoked by S3 event
// notifications. Each record gets its own pipeline invocation (its own DI scope, via
// envelope.Dispatch). Records are processed in order; the first unsuccessful one returns a Go error
// so AWS's async-invoke retry reprocesses the event rather than the failure being silently dropped.
// A malformed outer event is likewise a Go error - the sanctioned Lambda Runtime API error shape.
// On success no response body is written (S3 notifications are fire-and-forget).
func Handler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var evt s3Event
		if err := json.Unmarshal(event, &evt); err != nil {
			return nil, fmt.Errorf("awss3: malformed S3 event: %w", err)
		}

		for _, record := range evt.Records {
			if _, successful := envelope.DispatchResult(ctx, builder.Pipeline, builder.Container, resolveRequest(record)); !successful {
				return nil, fmt.Errorf("awss3: handling S3 event %q for %s/%s was not successful",
					record.EventName, record.S3.Bucket.Name, record.S3.Object.Key)
			}
		}

		return nil, nil
	}
}

// resolveRequest derives the wire.Request for one S3 record: the "{bucket}:{eventName}" topic, the
// object-metadata body, and the s3-prefixed headers.
func resolveRequest(record s3EventRecord) wire.Request {
	topic := record.EventName
	if record.S3.Bucket.Name != "" {
		topic = record.S3.Bucket.Name + ":" + record.EventName
	}

	key := decodeKey(record.S3.Object.Key)

	// s3Notification is a struct of scalars; json.Marshal cannot fail on it, so the error is ignored
	// (an empty body would be the only degradation, and it cannot occur here).
	body, _ := json.Marshal(s3Notification{
		EventName:  record.EventName,
		AwsRegion:  record.AwsRegion,
		BucketName: record.S3.Bucket.Name,
		Key:        key,
		Size:       record.S3.Object.Size,
		ETag:       record.S3.Object.ETag,
	})

	return wire.Request{Topic: topic, Headers: recordHeaders(record, key), Body: string(body)}
}

// decodeKey URL-decodes an S3 object key, which arrives URL-encoded in an event notification (a
// space as "+", other characters percent-encoded). It falls back to the raw value if the key is not
// valid encoding - S3 always sends a valid encoding, so that path is defensive, not a real case.
func decodeKey(raw string) string {
	if decoded, err := url.QueryUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// recordHeaders exposes the S3 record's metadata as s3-prefixed headers (key already URL-decoded),
// omitting any that are empty.
func recordHeaders(record s3EventRecord, key string) map[string]string {
	headers := map[string]string{}
	add := func(name, value string) {
		if value != "" {
			headers[name] = value
		}
	}
	add("s3-event-name", record.EventName)
	add("s3-aws-region", record.AwsRegion)
	add("s3-bucket", record.S3.Bucket.Name)
	add("s3-key", key)
	if record.S3.Object.Size > 0 {
		add("s3-size", strconv.FormatInt(record.S3.Object.Size, 10))
	}
	add("s3-etag", record.S3.Object.ETag)
	return headers
}
