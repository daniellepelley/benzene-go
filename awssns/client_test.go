package awssns

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sns"

	benzene "github.com/daniellepelley/benzene-go"
)

type fakePublishAPI struct {
	input   *sns.PublishInput
	err     error
	calls   int
	lastCtx context.Context
}

func (f *fakePublishAPI) Publish(ctx context.Context, params *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.calls++
	f.input = params
	f.lastCtx = ctx
	if f.err != nil {
		return nil, f.err
	}
	return &sns.PublishOutput{}, nil
}

func TestClient_SendSuccessReturnsAccepted(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"x-correlation-id": "abc"}, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusAccepted {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if api.calls != 1 {
		t.Fatalf("calls = %d, want 1", api.calls)
	}
	if *api.input.TopicArn != "arn:aws:sns:us-east-1:123456789012:example" {
		t.Errorf("TopicArn = %q, want %q", *api.input.TopicArn, "arn:aws:sns:us-east-1:123456789012:example")
	}
	if *api.input.Message != `{"name":"World"}` {
		t.Errorf("Message = %q, want %q", *api.input.Message, `{"name":"World"}`)
	}
}

func TestClient_SendWritesTopicAsMessageAttribute(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example")

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	attr, ok := api.input.MessageAttributes["topic"]
	if !ok {
		t.Fatal(`MessageAttributes["topic"] missing`)
	}
	if *attr.StringValue != "greet" {
		t.Errorf(`MessageAttributes["topic"].StringValue = %q, want "greet"`, *attr.StringValue)
	}
	if *attr.DataType != "String" {
		t.Errorf(`MessageAttributes["topic"].DataType = %q, want "String"`, *attr.DataType)
	}
}

func TestClient_SendWritesHeadersAsMessageAttributes(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example")

	c.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"x-correlation-id": "abc"}, []byte("{}"))

	attr, ok := api.input.MessageAttributes["x-correlation-id"]
	if !ok {
		t.Fatal(`MessageAttributes["x-correlation-id"] missing`)
	}
	if *attr.StringValue != "abc" {
		t.Errorf(`MessageAttributes["x-correlation-id"].StringValue = %q, want "abc"`, *attr.StringValue)
	}
}

func TestClient_TransportFailureIsServiceUnavailable(t *testing.T) {
	api := &fakePublishAPI{err: errors.New("boom")}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("ServiceUnavailable result should carry an error message")
	}
}

func TestClient_MessageGroupIDIsSetWhenConfigured(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example.fifo")
	c.MessageGroupID = func(topic benzene.Topic) string { return topic.ID }

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if api.input.MessageGroupId == nil || *api.input.MessageGroupId != "greet" {
		t.Errorf("MessageGroupId = %v, want %q", api.input.MessageGroupId, "greet")
	}
}

func TestClient_MessageGroupIDOmittedWhenNotConfigured(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example")

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if api.input.MessageGroupId != nil {
		t.Errorf("MessageGroupId = %v, want nil", api.input.MessageGroupId)
	}
}

func TestClient_MessageDeduplicationIDIsSetWhenConfigured(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example.fifo")
	c.MessageDeduplicationID = func(topic benzene.Topic, message []byte) string { return topic.ID + ":" + string(message) }

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("body"))

	want := "greet:body"
	if api.input.MessageDeduplicationId == nil || *api.input.MessageDeduplicationId != want {
		t.Errorf("MessageDeduplicationId = %v, want %q", api.input.MessageDeduplicationId, want)
	}
}

func TestClient_MessageDeduplicationIDOmittedWhenNotConfigured(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example")

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if api.input.MessageDeduplicationId != nil {
		t.Errorf("MessageDeduplicationId = %v, want nil", api.input.MessageDeduplicationId)
	}
}

func TestClient_ContextIsForwardedToAPI(t *testing.T) {
	api := &fakePublishAPI{}
	c := NewClient(api, "arn:aws:sns:us-east-1:123456789012:example")

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "value")
	c.Send(ctx, benzene.NewTopic("greet"), nil, []byte("{}"))

	if api.lastCtx.Value(ctxKey{}) != "value" {
		t.Error("Send should forward the caller's context to the API call")
	}
}
