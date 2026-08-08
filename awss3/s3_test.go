package awss3

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// record builds one S3 event-notification record for the given bucket/event/key.
func record(bucket, eventName, key string) s3EventRecord {
	return s3EventRecord{
		EventSource: "aws:s3",
		EventName:   eventName,
		AwsRegion:   "us-east-1",
		S3: s3Entity{
			Bucket: s3Bucket{Name: bucket},
			Object: s3Object{Key: key, Size: 42, ETag: "abc123"},
		},
	}
}

func TestResolveRequest_TopicIsBucketQualified(t *testing.T) {
	req := resolveRequest(record("uploads", "ObjectCreated:Put", "photo.jpg"))
	if req.Topic != "uploads:ObjectCreated:Put" {
		t.Errorf("Topic = %q, want %q", req.Topic, "uploads:ObjectCreated:Put")
	}

	// No bucket -> the bare event name.
	noBucket := record("", "ObjectCreated:Put", "photo.jpg")
	if got := resolveRequest(noBucket).Topic; got != "ObjectCreated:Put" {
		t.Errorf("Topic = %q, want the bare event name", got)
	}
}

func TestResolveRequest_BodyIsObjectMetadata(t *testing.T) {
	req := resolveRequest(record("uploads", "ObjectCreated:Put", "photo.jpg"))
	var got s3Notification
	if err := json.Unmarshal([]byte(req.Body), &got); err != nil {
		t.Fatalf("body is not an s3Notification: %v; body = %q", err, req.Body)
	}
	want := s3Notification{EventName: "ObjectCreated:Put", AwsRegion: "us-east-1", BucketName: "uploads", Key: "photo.jpg", Size: 42, ETag: "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body = %+v, want %+v", got, want)
	}
}

func TestResolveRequest_Headers(t *testing.T) {
	req := resolveRequest(record("uploads", "ObjectRemoved:Delete", "old.txt"))
	want := map[string]string{
		"s3-event-name": "ObjectRemoved:Delete",
		"s3-aws-region": "us-east-1",
		"s3-bucket":     "uploads",
		"s3-key":        "old.txt",
		"s3-size":       "42",
		"s3-etag":       "abc123",
	}
	for k, v := range want {
		if req.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
		}
	}
}

func TestResolveRequest_ZeroSizeOmitsSizeHeader(t *testing.T) {
	r := record("uploads", "ObjectRemoved:Delete", "gone.txt")
	r.S3.Object.Size = 0
	if _, present := resolveRequest(r).Headers["s3-size"]; present {
		t.Error("s3-size header present for a zero-size object, want omitted")
	}
}

func buildApp(register func(*benzene.Registry)) *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	register(registry)
	return &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
}

func s3Event2(records ...s3EventRecord) json.RawMessage {
	data, _ := json.Marshal(s3Event{Records: records})
	return data
}

func TestHandler_AllSuccessReturnsNoError(t *testing.T) {
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("uploads:ObjectCreated:Put"), benzene.Handler[s3Notification, struct{}](
			func(context.Context, s3Notification) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) }))
	})

	out, err := Handler(app)(context.Background(), s3Event2(
		record("uploads", "ObjectCreated:Put", "a.jpg"),
		record("uploads", "ObjectCreated:Put", "b.jpg"),
	))
	if err != nil {
		t.Fatalf("Handler() error = %v, want nil", err)
	}
	if out != nil {
		t.Errorf("response = %s, want nil (fire-and-forget)", out)
	}
}

func TestHandler_FailedRecordReturnsGoErrorForAsyncRetry(t *testing.T) {
	seen := 0
	app := buildApp(func(r *benzene.Registry) {
		benzene.Register(r, benzene.NewTopic("uploads:ObjectCreated:Put"), benzene.Handler[s3Notification, struct{}](
			func(_ context.Context, n s3Notification) benzene.Result[struct{}] {
				seen++
				if n.Key == "bad.jpg" {
					return benzene.ServiceUnavailable[struct{}]("downstream down")
				}
				return benzene.Ok(struct{}{})
			}))
	})

	_, err := Handler(app)(context.Background(), s3Event2(
		record("uploads", "ObjectCreated:Put", "good.jpg"),
		record("uploads", "ObjectCreated:Put", "bad.jpg"),
		record("uploads", "ObjectCreated:Put", "after.jpg"),
	))
	if err == nil {
		t.Fatal("Handler() error = nil, want a Go error so AWS async-invoke retries the event")
	}
	if seen != 2 {
		t.Errorf("handler ran %d times, want 2 - processing must stop at the first failure", seen)
	}
}

func TestHandler_UnroutedTopicReturnsGoError(t *testing.T) {
	app := buildApp(func(*benzene.Registry) {})
	if _, err := Handler(app)(context.Background(), s3Event2(record("uploads", "ObjectCreated:Put", "a.jpg"))); err == nil {
		t.Error("Handler() with no registered handler = nil error, want a Go error (not-found is unsuccessful)")
	}
}

func TestHandler_MalformedEventReturnsGoError(t *testing.T) {
	app := buildApp(func(*benzene.Registry) {})
	if _, err := Handler(app)(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("Handler() with a malformed event = nil error, want a Go error")
	}
}
