package awskafka

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// record builds one Kafka record for the given topic/partition/offset with a base64-encoded value.
func record(topic string, partition int, offset int64, value string) kafkaRecord {
	return kafkaRecord{
		Topic:     topic,
		Partition: partition,
		Offset:    offset,
		Timestamp: 1700000000000,
		Value:     base64.StdEncoding.EncodeToString([]byte(value)),
	}
}

func TestResolveRequest_TopicBodyHeaders(t *testing.T) {
	r := record("orders", 0, 5, `{"id":"a"}`)
	// Each header is a single-entry map of name -> its bytes as a JSON int array (the Kafka header
	// wire form AWS delivers).
	r.Headers = []map[string][]int{{"h1": bytesToInts([]byte("v1"))}, {"h2": bytesToInts([]byte("v2"))}}

	req, err := resolveRequest(r)
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	if req.Topic != "orders" {
		t.Errorf("Topic = %q, want %q", req.Topic, "orders")
	}
	if req.Body != `{"id":"a"}` {
		t.Errorf("Body = %q, want the base64-decoded value", req.Body)
	}
	if req.Headers["h1"] != "v1" || req.Headers["h2"] != "v2" {
		t.Errorf("Headers = %v, want the UTF-8 decoded Kafka headers", req.Headers)
	}
}

func TestResolveRequest_DecodeFailure(t *testing.T) {
	r := record("orders", 0, 1, "")
	r.Value = "!!!not-base64!!!"
	if _, err := resolveRequest(r); err == nil {
		t.Error("resolveRequest() with undecodable value = nil error, want an error")
	}
}

func TestResolveRequest_EmptyValueIsEmptyBody(t *testing.T) {
	// A tombstone (empty value) decodes to an empty body without error.
	req, err := resolveRequest(record("orders", 0, 1, ""))
	if err != nil {
		t.Fatalf("resolveRequest() error = %v", err)
	}
	if req.Body != "" {
		t.Errorf("Body = %q, want empty", req.Body)
	}
}

func bytesToInts(b []byte) []int {
	out := make([]int, len(b))
	for i, c := range b {
		out[i] = int(c)
	}
	return out
}

func buildApp(register func(*benzene.Registry)) *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	register(registry)
	return &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
}

func kafkaEventJSON(records map[string][]kafkaRecord) json.RawMessage {
	data, _ := json.Marshal(kafkaEvent{EventSource: "aws:kafka", Records: records})
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

	out, err := Handler(app)(context.Background(), kafkaEventJSON(map[string][]kafkaRecord{
		"orders-0": {record("orders", 0, 1, `{"id":"a"}`), record("orders", 0, 2, `{"id":"b"}`)},
	}))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 0 {
		t.Errorf("BatchItemFailures = %+v, want empty", failures)
	}
}

func TestHandler_StopsAtFirstFailureInOffsetOrder(t *testing.T) {
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

	// Records deliberately out of offset order in the slice; the binding must sort by offset, so the
	// failure is at offset 6 and offset 7 is left for redelivery.
	out, err := Handler(app)(context.Background(), kafkaEventJSON(map[string][]kafkaRecord{
		"orders-0": {
			record("orders", 0, 7, `{"id":"c"}`),
			record("orders", 0, 5, `{"id":"a"}`),
			record("orders", 0, 6, `{"fail":true}`),
		},
	}))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	failures := decodeBatch(t, out).BatchItemFailures
	if len(failures) != 1 || failures[0].ItemIdentifier.Partition != "orders-0" || failures[0].ItemIdentifier.Offset != 6 {
		t.Errorf("BatchItemFailures = %+v, want [{orders-0, 6}]", failures)
	}
	if seen != 2 {
		t.Errorf("handler ran %d times, want 2 - offset 7 must be left for redelivery", seen)
	}
}

func TestHandler_PartitionsAreIndependent(t *testing.T) {
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("orders"), benzene.Handler[map[string]any, struct{}](
			func(_ context.Context, m map[string]any) benzene.Result[struct{}] {
				if m["fail"] == true {
					return benzene.ServiceUnavailable[struct{}]("down")
				}
				return benzene.Ok(struct{}{})
			}))
	})

	out, err := Handler(app)(context.Background(), kafkaEventJSON(map[string][]kafkaRecord{
		"orders-0": {record("orders", 0, 10, `{"fail":true}`)}, // fails at offset 10
		"orders-1": {record("orders", 1, 3, `{"id":"ok"}`)},    // succeeds
	}))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	failures := decodeBatch(t, out).BatchItemFailures
	if len(failures) != 1 || failures[0].ItemIdentifier.Partition != "orders-0" || failures[0].ItemIdentifier.Offset != 10 {
		t.Errorf("BatchItemFailures = %+v, want only [{orders-0, 10}] (orders-1 succeeded independently)", failures)
	}
}

func TestHandler_PoisonRecordReportsFailure(t *testing.T) {
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("orders"), benzene.Handler[map[string]any, struct{}](
			func(context.Context, map[string]any) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) }))
	})
	poison := record("orders", 0, 4, "")
	poison.Value = "!!!not-base64!!!"
	out, err := Handler(app)(context.Background(), kafkaEventJSON(map[string][]kafkaRecord{"orders-0": {poison}}))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 1 || failures[0].ItemIdentifier.Offset != 4 {
		t.Errorf("BatchItemFailures = %+v, want [{orders-0, 4}] for a poison record", failures)
	}
}

func TestHandler_UnroutedTopicIsFailure(t *testing.T) {
	app := buildApp(func(*benzene.Registry) {})
	out, err := Handler(app)(context.Background(), kafkaEventJSON(map[string][]kafkaRecord{"orders-0": {record("orders", 0, 1, `{}`)}}))
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	if failures := decodeBatch(t, out).BatchItemFailures; len(failures) != 1 || failures[0].ItemIdentifier.Offset != 1 {
		t.Errorf("BatchItemFailures = %+v, want [{orders-0, 1}]", failures)
	}
}

func TestHandler_MalformedEventReturnsGoError(t *testing.T) {
	app := buildApp(func(*benzene.Registry) {})
	if _, err := Handler(app)(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("Handler() with a malformed event = nil error, want a Go error")
	}
}
