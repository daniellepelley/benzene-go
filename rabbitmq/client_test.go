package rabbitmq

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// fakePublisher records the one publish it receives and can be told to fail.
type fakePublisher struct {
	published []publishCall
	err       error
}

type publishCall struct {
	exchange  string
	key       string
	mandatory bool
	immediate bool
	msg       amqp.Publishing
}

func (p *fakePublisher) PublishWithContext(_ context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, publishCall{exchange, key, mandatory, immediate, msg})
	return nil
}

func headerBytes(t *testing.T, table amqp.Table, key string) string {
	t.Helper()
	v, ok := table[key]
	if !ok {
		t.Fatalf("headers table has no %q key; table = %v", key, table)
	}
	b, ok := v.([]byte)
	if !ok {
		t.Fatalf("header %q = %T, want []byte (RabbitMQ's wire form)", key, v)
	}
	return string(b)
}

func TestClient_SendPublishesTopicAsRoutingKeyAndHeader(t *testing.T) {
	pub := &fakePublisher{}
	client := NewClient(pub, "orders")

	result := client.Send(context.Background(), benzene.NewTopic("order:create"),
		map[string]string{"x-correlation-id": "abc"}, []byte(`{"id":"1"}`))

	if result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if len(pub.published) != 1 {
		t.Fatalf("published %d messages, want 1", len(pub.published))
	}
	call := pub.published[0]
	if call.exchange != "orders" {
		t.Errorf("exchange = %q, want %q", call.exchange, "orders")
	}
	if call.key != "order:create" {
		t.Errorf("routing key = %q, want the topic verbatim", call.key)
	}
	if call.mandatory || call.immediate {
		t.Errorf("mandatory/immediate = %v/%v, want false/false by default", call.mandatory, call.immediate)
	}
	if got := headerBytes(t, call.msg.Headers, "topic"); got != "order:create" {
		t.Errorf(`header "topic" = %q, want the topic carried as a header too`, got)
	}
	if got := headerBytes(t, call.msg.Headers, "x-correlation-id"); got != "abc" {
		t.Errorf(`header "x-correlation-id" = %q, want %q`, got, "abc")
	}
	if string(call.msg.Body) != `{"id":"1"}` {
		t.Errorf("Body = %q, want the message verbatim, not enveloped", call.msg.Body)
	}
	if call.msg.DeliveryMode != amqp.Persistent {
		t.Errorf("DeliveryMode = %d, want %d (persistent) from NewClient's default", call.msg.DeliveryMode, amqp.Persistent)
	}
	if call.msg.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want application/json", call.msg.ContentType)
	}
}

func TestClient_TransientDeliveryModeWhenNotPersistent(t *testing.T) {
	pub := &fakePublisher{}
	// A zero-value Client (not built via NewClient) publishes transiently.
	client := &Client{Publisher: pub, Exchange: ""}

	client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if got := pub.published[0].msg.DeliveryMode; got != amqp.Transient {
		t.Errorf("DeliveryMode = %d, want %d (transient) when Persistent is false", got, amqp.Transient)
	}
	if pub.published[0].exchange != "" {
		t.Errorf("exchange = %q, want the default exchange (empty)", pub.published[0].exchange)
	}
}

func TestClient_MandatoryIsForwarded(t *testing.T) {
	pub := &fakePublisher{}
	client := NewClient(pub, "orders")
	client.Mandatory = true

	client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if !pub.published[0].mandatory {
		t.Error("mandatory = false, want true when Client.Mandatory is set")
	}
}

func TestClient_ReservedNamesOverridesTopicHeaderKey(t *testing.T) {
	pub := &fakePublisher{}
	client := NewClient(pub, "orders")
	client.ReservedNames = wire.ReservedNames{TopicKey: "benzene-topic"}

	client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	table := pub.published[0].msg.Headers
	if got := headerBytes(t, table, "benzene-topic"); got != "greet" {
		t.Errorf(`header "benzene-topic" = %q, want the topic under the overridden key`, got)
	}
	if _, ok := table["topic"]; ok {
		t.Error(`default "topic" header should not be set when the key is overridden`)
	}
}

func TestClient_PublishFailureIsServiceUnavailable(t *testing.T) {
	pub := &fakePublisher{err: errors.New("channel closed")}
	client := NewClient(pub, "orders")

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("Errors should carry the transport failure detail")
	}
}
