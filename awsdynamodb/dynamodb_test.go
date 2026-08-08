package awsdynamodb

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestTableName(t *testing.T) {
	tests := map[string]string{
		"arn:aws:dynamodb:us-east-1:123456789012:table/orders/stream/2024-01-01T00:00:00.000": "orders",
		"arn:aws:dynamodb:eu-west-2:1:table/My-Table/stream/x":                                "My-Table",
		"":                               "",
		"arn:aws:dynamodb:us-east-1:123": "", // too few segments
		"arn:aws:dynamodb:us-east-1:123:index/orders/name": "", // not a table resource
		"arn:aws:dynamodb:us-east-1:123:table":             "", // no name after "table"
	}
	for arn, want := range tests {
		if got := tableName(arn); got != want {
			t.Errorf("tableName(%q) = %q, want %q", arn, got, want)
		}
	}
}

const ordersArn = "arn:aws:dynamodb:us-east-1:123456789012:table/orders/stream/2024-01-01T00:00:00.000"

func record(eventName, arn, seq string, dynamodb string) dynamoDBStreamRecord {
	r := dynamoDBStreamRecord{EventID: "evt-" + seq, EventName: eventName, EventSource: "aws:dynamodb", EventSourceArn: arn, AwsRegion: "us-east-1"}
	if dynamodb != "" {
		var data dynamoDBStreamData
		if err := json.Unmarshal([]byte(dynamodb), &data); err != nil {
			panic(err)
		}
		data.SequenceNumber = seq
		r.Dynamodb = &data
	}
	return r
}

func TestResolveRequest_Topic(t *testing.T) {
	req, err := resolveRequest(record("INSERT", ordersArn, "1", `{"NewImage":{"id":{"S":"a"}}}`))
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	if req.Topic != "orders:INSERT" {
		t.Errorf("Topic = %q, want %q", req.Topic, "orders:INSERT")
	}

	// No table in the ARN -> the bare event name.
	req, _ = resolveRequest(record("MODIFY", "not-an-arn", "2", `{"NewImage":{"id":{"S":"a"}}}`))
	if req.Topic != "MODIFY" {
		t.Errorf("Topic = %q, want the bare event name %q", req.Topic, "MODIFY")
	}
}

func TestResolveRequest_BodyPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		dynamodb string
		wantBody string
	}{
		{"new image wins", `{"NewImage":{"v":{"S":"new"}},"OldImage":{"v":{"S":"old"}},"Keys":{"id":{"N":"1"}}}`, `{"v":"new"}`},
		{"old image when no new", `{"OldImage":{"v":{"S":"old"}},"Keys":{"id":{"N":"1"}}}`, `{"v":"old"}`},
		{"keys when only keys", `{"Keys":{"id":{"N":"1"}}}`, `{"id":1}`},
		{"empty when no image", `{"SequenceNumber":"1"}`, ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := resolveRequest(record("INSERT", ordersArn, "1", tt.dynamodb))
			if err != nil {
				t.Fatalf("resolveRequest() error = %v", err)
			}
			if tt.wantBody == "" {
				if req.Body != "" {
					t.Errorf("Body = %q, want empty", req.Body)
				}
				return
			}
			if !reflect.DeepEqual(asAny(t, []byte(req.Body)), asAny(t, []byte(tt.wantBody))) {
				t.Errorf("Body = %s, want %s", req.Body, tt.wantBody)
			}
		})
	}
}

func TestResolveRequest_BodyEmptyWhenNoDynamodb(t *testing.T) {
	req, err := resolveRequest(record("REMOVE", ordersArn, "1", ""))
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	if req.Body != "" {
		t.Errorf("Body = %q, want empty when the record has no dynamodb object", req.Body)
	}
}

func TestResolveRequest_Headers(t *testing.T) {
	req, err := resolveRequest(record("INSERT", ordersArn, "seq-9", `{"NewImage":{"id":{"S":"a"}},"StreamViewType":"NEW_AND_OLD_IMAGES"}`))
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	want := map[string]string{
		"dynamodb-event-name":       "INSERT",
		"dynamodb-event-id":         "evt-seq-9",
		"dynamodb-table":            "orders",
		"dynamodb-sequence-number":  "seq-9",
		"dynamodb-stream-view-type": "NEW_AND_OLD_IMAGES",
		"dynamodb-event-source-arn": ordersArn,
		"dynamodb-aws-region":       "us-east-1",
	}
	for k, v := range want {
		if req.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
		}
	}
}

func TestResolveRequest_ImageConversionFailure(t *testing.T) {
	if _, err := resolveRequest(record("INSERT", ordersArn, "1", `{"NewImage":{"bad":{"N":"not-a-number"}}}`)); err == nil {
		t.Error("resolveRequest() with a malformed image = nil error, want an error")
	}
}

// buildApp wires a pipeline that routes to the given handlers.
func buildApp(register func(*benzene.Registry)) *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	register(registry)
	return &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
}

func streamEvent(records ...dynamoDBStreamRecord) json.RawMessage {
	data, _ := json.Marshal(dynamoDBEvent{Records: records})
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
		benzene.Register(r, benzene.NewTopic("orders:INSERT"), benzene.Handler[map[string]any, struct{}](
			func(context.Context, map[string]any) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) }))
	})

	out, err := Handler(app)(context.Background(), streamEvent(
		record("INSERT", ordersArn, "1", `{"NewImage":{"id":{"S":"a"}}}`),
		record("INSERT", ordersArn, "2", `{"NewImage":{"id":{"S":"b"}}}`),
	))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 0 {
		t.Errorf("BatchItemFailures = %+v, want empty", failures)
	}
}

func TestHandler_StopsAtFirstFailureAndReportsSequenceNumber(t *testing.T) {
	inserts := 0
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("orders:INSERT"), benzene.Handler[map[string]any, struct{}](
			func(context.Context, map[string]any) benzene.Result[struct{}] {
				inserts++
				return benzene.Ok(struct{}{})
			}))
		benzene.Register(r, benzene.NewTopic("orders:MODIFY"), benzene.Handler[map[string]any, struct{}](
			func(context.Context, map[string]any) benzene.Result[struct{}] {
				return benzene.ServiceUnavailable[struct{}]("downstream down")
			}))
	})

	out, err := Handler(app)(context.Background(), streamEvent(
		record("INSERT", ordersArn, "seq-1", `{"NewImage":{"id":{"S":"a"}}}`),
		record("MODIFY", ordersArn, "seq-2", `{"NewImage":{"id":{"S":"b"}}}`),
		record("INSERT", ordersArn, "seq-3", `{"NewImage":{"id":{"S":"c"}}}`),
	))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	failures := decodeBatch(t, out).BatchItemFailures
	if len(failures) != 1 || failures[0].ItemIdentifier != "seq-2" {
		t.Errorf("BatchItemFailures = %+v, want exactly [seq-2]", failures)
	}
	if inserts != 1 {
		t.Errorf("INSERT handler ran %d times, want 1 - the record after the failure must be left for redelivery", inserts)
	}
}

func TestHandler_UnroutedTopicIsNotFoundAndStops(t *testing.T) {
	// No handler registered for the topic -> the router returns not-found (unsuccessful) -> the
	// record is reported for redelivery, not silently checkpointed past.
	app := buildApp(func(*benzene.Registry) {})
	out, err := Handler(app)(context.Background(), streamEvent(
		record("INSERT", ordersArn, "seq-1", `{"NewImage":{"id":{"S":"a"}}}`),
	))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 1 || failures[0].ItemIdentifier != "seq-1" {
		t.Errorf("BatchItemFailures = %+v, want [seq-1]", failures)
	}
}

func TestHandler_ImageConversionFailureReportsBatchFailure(t *testing.T) {
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("orders:INSERT"), benzene.Handler[map[string]any, struct{}](
			func(context.Context, map[string]any) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) }))
	})
	out, err := Handler(app)(context.Background(), streamEvent(
		record("INSERT", ordersArn, "seq-1", `{"NewImage":{"bad":{"N":"not-a-number"}}}`),
	))
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

func TestFailAt_FallsBackToEventIDWithoutSequenceNumber(t *testing.T) {
	// A record with no dynamodb object has no sequence number: fall back to the event id.
	out := failAt(record("REMOVE", ordersArn, "unused", ""))
	var resp batchResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "evt-unused" {
		t.Errorf("BatchItemFailures = %+v, want the event id fallback [evt-unused]", resp.BatchItemFailures)
	}
}
