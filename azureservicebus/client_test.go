package azureservicebus

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// fakeSender is an in-memory SenderAPI: it records the last message sent and can inject a send error.
type fakeSender struct {
	sendErr error
	sent    *azservicebus.Message
	calls   int
}

func (f *fakeSender) SendMessage(_ context.Context, message *azservicebus.Message, _ *azservicebus.SendMessageOptions) error {
	f.calls++
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = message
	return nil
}

func TestClient_SendSuccessWritesTopicAndHeaders(t *testing.T) {
	api := &fakeSender{}
	c := NewClient(api)

	result := c.Send(context.Background(), benzene.NewTopic("greet"),
		map[string]string{"x-correlation-id": "abc"}, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusAccepted {
		t.Errorf("Status = %q, want %q (a successful send is accepted)", result.Status, benzene.StatusAccepted)
	}
	if api.sent == nil {
		t.Fatal("no message sent")
	}
	if string(api.sent.Body) != `{"name":"World"}` {
		t.Errorf("Body = %q, want the message bytes verbatim", api.sent.Body)
	}
	if got := api.sent.ApplicationProperties["topic"]; got != "greet" {
		t.Errorf("topic property = %v, want greet", got)
	}
	if got := api.sent.ApplicationProperties["x-correlation-id"]; got != "abc" {
		t.Errorf("header property = %v, want abc", got)
	}
}

func TestClient_SendFailureMapsToServiceUnavailable(t *testing.T) {
	api := &fakeSender{sendErr: errors.New("broker down")}
	c := NewClient(api)

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q on a send error", result.Status, benzene.StatusServiceUnavailable)
	}
	if result.IsSuccessful() {
		t.Error("expected a failure result on a send error")
	}
}

func TestClient_SendUsesConfiguredTopicKey(t *testing.T) {
	api := &fakeSender{}
	c := &Client{API: api, ReservedNames: wire.ReservedNames{TopicKey: "x-topic"}}

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if got := api.sent.ApplicationProperties["x-topic"]; got != "greet" {
		t.Errorf("topic under custom key = %v, want greet", got)
	}
	if _, ok := api.sent.ApplicationProperties["topic"]; ok {
		t.Error("default topic key must not be set when a custom key is configured")
	}
}

func TestClient_SendTopicPropertyWinsOverStrayHeader(t *testing.T) {
	// A header colliding with the topic key must not shadow the real topic - the topic is written last.
	api := &fakeSender{}
	c := NewClient(api)

	c.Send(context.Background(), benzene.NewTopic("greet"),
		map[string]string{"topic": "wrong"}, []byte(`{}`))

	if got := api.sent.ApplicationProperties["topic"]; got != "greet" {
		t.Errorf("topic property = %v, want greet (topic wins over a stray header)", got)
	}
}
