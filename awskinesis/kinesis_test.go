package awskinesis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestStreamName(t *testing.T) {
	tests := map[string]string{
		"arn:aws:kinesis:us-east-1:123456789012:stream/orders": "orders",
		"arn:aws:kinesis:eu-west-2:1:stream/My-Stream":         "My-Stream",
		"":                              "",
		"arn:aws:kinesis:us-east-1:123": "", // too few segments
		"arn:aws:kinesis:us-east-1:123:consumer/orders/x": "", // not a stream resource
		"arn:aws:kinesis:us-east-1:123:stream":            "", // no name after "stream"
		"arn:aws:kinesis:us-east-1:123:stream/":           "", // empty name
	}
	for arn, want := range tests {
		if got := streamName(arn); got != want {
			t.Errorf("streamName(%q) = %q, want %q", arn, got, want)
		}
	}
}

const ordersArn = "arn:aws:kinesis:us-east-1:123456789012:stream/orders"

// record builds one Kinesis stream record with the given sequence number and a payload that is
// base64-encoded into the data field (as Lambda delivers it). An empty payload leaves the kinesis
// object present but with no data; a nil kinesis omits the object entirely (data == "-").
func record(seq, payload string) kinesisStreamRecord {
	r := kinesisStreamRecord{EventID: "evt-" + seq, EventName: "aws:kinesis:record", EventSource: "aws:kinesis", EventSourceArn: ordersArn, AwsRegion: "us-east-1"}
	if payload != "-" {
		r.Kinesis = &kinesisRecordData{PartitionKey: "pk-" + seq, SequenceNumber: seq, Data: base64.StdEncoding.EncodeToString([]byte(payload))}
	}
	return r
}

func TestResolveRequest_Topic(t *testing.T) {
	req, err := resolveRequest(record("1", `{"id":"a"}`))
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	if req.Topic != "orders" {
		t.Errorf("Topic = %q, want the stream name %q", req.Topic, "orders")
	}

	// No stream in the ARN -> the bare event name.
	noArn := record("2", `{"id":"b"}`)
	noArn.EventSourceArn = "not-an-arn"
	req, _ = resolveRequest(noArn)
	if req.Topic != "aws:kinesis:record" {
		t.Errorf("Topic = %q, want the bare event name", req.Topic)
	}
}

func TestResolveRequest_BodyIsBase64Decoded(t *testing.T) {
	req, err := resolveRequest(record("1", `{"id":"a","amount":9.99}`))
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(req.Body), &got); err != nil {
		t.Fatalf("body is not the decoded JSON: %v; body = %q", err, req.Body)
	}
	if got["id"] != "a" {
		t.Errorf("decoded body = %v, want the producer's JSON", got)
	}
}

func TestResolveRequest_EmptyBodyWhenNoData(t *testing.T) {
	// A record with no kinesis object, and one with an empty data field, both yield an empty body.
	for _, r := range []kinesisStreamRecord{record("1", "-"), record("2", "")} {
		req, err := resolveRequest(r)
		if err != nil {
			t.Fatalf("resolveRequest() error = %v", err)
		}
		if req.Body != "" {
			t.Errorf("Body = %q, want empty", req.Body)
		}
	}
}

func TestResolveRequest_Headers(t *testing.T) {
	r := record("seq-9", `{"id":"a"}`)
	r.Kinesis.ApproximateArrivalTimestamp = 1700000000.5
	req, err := resolveRequest(r)
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	want := map[string]string{
		"kinesis-event-id":                      "evt-seq-9",
		"kinesis-event-name":                    "aws:kinesis:record",
		"kinesis-stream":                        "orders",
		"kinesis-partition-key":                 "pk-seq-9",
		"kinesis-sequence-number":               "seq-9",
		"kinesis-approximate-arrival-timestamp": "1700000000.5",
		"kinesis-event-source-arn":              ordersArn,
		"kinesis-aws-region":                    "us-east-1",
	}
	for k, v := range want {
		if req.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
		}
	}
}

func TestResolveRequest_DecodeFailure(t *testing.T) {
	r := record("1", "-")
	r.Kinesis = &kinesisRecordData{SequenceNumber: "1", Data: "!!!not-base64!!!"}
	if _, err := resolveRequest(r); err == nil {
		t.Error("resolveRequest() with undecodable data = nil error, want an error")
	}
}

func buildApp(register func(*benzene.Registry)) *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	register(registry)
	return &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
}

func streamEvent(records ...kinesisStreamRecord) json.RawMessage {
	data, _ := json.Marshal(kinesisEvent{Records: records})
	return data
}

func decodeBatch(t *testing.T, raw json.RawMessage) batchResponse {
	t.Helper()
	var resp batchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal batch response %s: %v", raw, err)
	}
	return resp
}

func TestHandler_AllSuccessReportsNoFailures(t *testing.T) {
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("orders"), benzene.Handler[map[string]any, struct{}](
			func(context.Context, map[string]any) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) }))
	})

	out, err := Handler(app)(context.Background(), streamEvent(record("1", `{"id":"a"}`), record("2", `{"id":"b"}`)))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 0 {
		t.Errorf("BatchItemFailures = %+v, want empty", failures)
	}
}

func TestHandler_StopsAtFirstFailureAndReportsSequenceNumber(t *testing.T) {
	seen := 0
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("orders"), benzene.Handler[map[string]any, struct{}](
			func(_ context.Context, m map[string]any) benzene.Result[struct{}] {
				seen++
				if m["fail"] == true {
					return benzene.ServiceUnavailable[struct{}]("downstream down")
				}
				return benzene.Ok(struct{}{})
			}))
	})

	out, err := Handler(app)(context.Background(), streamEvent(
		record("seq-1", `{"id":"a"}`),
		record("seq-2", `{"fail":true}`),
		record("seq-3", `{"id":"c"}`),
	))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	failures := decodeBatch(t, out).BatchItemFailures
	if len(failures) != 1 || failures[0].ItemIdentifier != "seq-2" {
		t.Errorf("BatchItemFailures = %+v, want exactly [seq-2]", failures)
	}
	if seen != 2 {
		t.Errorf("handler ran %d times, want 2 - the record after the failure must be left for redelivery", seen)
	}
}

func TestHandler_UnroutedTopicIsNotFoundAndStops(t *testing.T) {
	app := buildApp(func(*benzene.Registry) {})
	out, err := Handler(app)(context.Background(), streamEvent(record("seq-1", `{"id":"a"}`)))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 1 || failures[0].ItemIdentifier != "seq-1" {
		t.Errorf("BatchItemFailures = %+v, want [seq-1]", failures)
	}
}

func TestHandler_DecodeFailureReportsBatchFailure(t *testing.T) {
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("orders"), benzene.Handler[map[string]any, struct{}](
			func(context.Context, map[string]any) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) }))
	})
	poison := record("seq-1", "-")
	poison.Kinesis = &kinesisRecordData{SequenceNumber: "seq-1", Data: "!!!not-base64!!!"}
	out, err := Handler(app)(context.Background(), streamEvent(poison))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 1 || failures[0].ItemIdentifier != "seq-1" {
		t.Errorf("BatchItemFailures = %+v, want [seq-1] for a poison record", failures)
	}
}

func TestHandler_MalformedEventReturnsGoError(t *testing.T) {
	app := buildApp(func(*benzene.Registry) {})
	if _, err := Handler(app)(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("Handler() with a malformed event = nil error, want a Go error (the Lambda runtime error shape)")
	}
}

func TestFailAt_FallsBackToEventIDWithoutKinesis(t *testing.T) {
	out := failAt(record("unused", "-"))
	var resp batchResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "evt-unused" {
		t.Errorf("BatchItemFailures = %+v, want the event id fallback [evt-unused]", resp.BatchItemFailures)
	}
}

// A base64 round trip sanity check the tests rely on.
func TestRecordHelperEncodesData(t *testing.T) {
	r := record("1", `{"x":1}`)
	if _, err := base64.StdEncoding.DecodeString(r.Kinesis.Data); err != nil {
		t.Fatalf("record helper did not base64-encode the payload: %v", err)
	}
	if !reflect.DeepEqual(r.Kinesis.PartitionKey, "pk-1") {
		t.Errorf("partition key = %q", r.Kinesis.PartitionKey)
	}
}
