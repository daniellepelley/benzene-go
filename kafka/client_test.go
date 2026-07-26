package kafka

import (
	"context"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	kafkago "github.com/segmentio/kafka-go"
)

type fakeWriter struct {
	written  []kafkago.Message
	writeErr error
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	if w.writeErr != nil {
		return w.writeErr
	}
	w.written = append(w.written, msgs...)
	return nil
}

func headerValue(t *testing.T, msg kafkago.Message, key string) string {
	t.Helper()
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	t.Fatalf("message has no %q header; headers = %v", key, msg.Headers)
	return ""
}

func TestClient_SendWritesToTopicWithHeadersAndBody(t *testing.T) {
	writer := &fakeWriter{}
	client := NewClient(writer)

	result := client.Send(context.Background(), benzene.NewTopic("greet"),
		map[string]string{"x-correlation-id": "abc"}, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if len(writer.written) != 1 {
		t.Fatalf("wrote %d messages, want 1", len(writer.written))
	}
	msg := writer.written[0]
	if msg.Topic != "greet" {
		t.Errorf("Topic = %q, want %q (the message's own Topic field, not a header)", msg.Topic, "greet")
	}
	if got := headerValue(t, msg, "x-correlation-id"); got != "abc" {
		t.Errorf(`header "x-correlation-id" = %q, want %q`, got, "abc")
	}
	if len(msg.Headers) != 1 {
		t.Errorf("Headers = %v, want exactly the one caller-supplied header (no topic header embedded)", msg.Headers)
	}
	if string(msg.Value) != `{"name":"World"}` {
		t.Errorf("Value = %q, want the message body verbatim, not wrapped in an envelope", msg.Value)
	}
	if msg.Key != nil {
		t.Errorf("Key = %q, want nil when no Key func is configured", msg.Key)
	}
}

func TestClient_KeyFuncSetsMessageKey(t *testing.T) {
	writer := &fakeWriter{}
	client := NewClient(writer)
	client.Key = func(topic benzene.Topic, _ []byte) []byte { return []byte(topic.String()) }

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if got := string(writer.written[0].Key); got != "greet" {
		t.Errorf("Key = %q, want %q", got, "greet")
	}
}

func TestClient_WriteFailureIsServiceUnavailable(t *testing.T) {
	writer := &fakeWriter{writeErr: errors.New("broker gone")}
	client := NewClient(writer)

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("Errors should carry the transport failure detail")
	}
}
