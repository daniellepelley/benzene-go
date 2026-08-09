package azureeventhub

import (
	"context"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// fakeProducer records the last SendEvent call and can be told to fail. No live Azure and no SDK
// import - the whole point of the ProducerAPI seam.
type fakeProducer struct {
	err          error
	calls        int
	lastCtx      context.Context
	lastBody     []byte
	lastProps    map[string]any
	lastPartKey  *string
	partKeyValue string
	partKeySet   bool
}

func (f *fakeProducer) SendEvent(ctx context.Context, body []byte, properties map[string]any, partitionKey *string) error {
	f.calls++
	f.lastCtx = ctx
	f.lastBody = body
	f.lastProps = properties
	f.lastPartKey = partitionKey
	f.partKeySet = partitionKey != nil
	if partitionKey != nil {
		f.partKeyValue = *partitionKey
	}
	return f.err
}

func TestClient_SendSuccessReturnsAccepted(t *testing.T) {
	p := &fakeProducer{}
	c := NewClient(p)

	result := c.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"x-correlation-id": "abc"}, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusAccepted {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if p.calls != 1 {
		t.Fatalf("calls = %d, want 1", p.calls)
	}
	if string(p.lastBody) != `{"name":"World"}` {
		t.Errorf("body = %q, want %q", string(p.lastBody), `{"name":"World"}`)
	}
}

func TestClient_SendWritesTopicAsEventProperty(t *testing.T) {
	p := &fakeProducer{}
	c := NewClient(p)

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if got := p.lastProps["topic"]; got != "greet" {
		t.Errorf(`properties["topic"] = %v, want "greet"`, got)
	}
}

func TestClient_SendWritesTopicUnderTheConfiguredReservedKey(t *testing.T) {
	p := &fakeProducer{}
	c := NewClient(p)
	c.ReservedNames = wire.ReservedNames{TopicKey: "x-my-topic"}

	c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if got := p.lastProps["x-my-topic"]; got != "greet" {
		t.Errorf(`properties["x-my-topic"] = %v, want "greet"`, got)
	}
	if _, ok := p.lastProps["topic"]; ok {
		t.Error(`the default "topic" property must not be written when the key is overridden`)
	}
}

func TestClient_SendWritesHeadersAsEventProperties(t *testing.T) {
	p := &fakeProducer{}
	c := NewClient(p)

	c.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"x-correlation-id": "abc"}, []byte("{}"))

	if got := p.lastProps["x-correlation-id"]; got != "abc" {
		t.Errorf(`properties["x-correlation-id"] = %v, want "abc"`, got)
	}
}

func TestClient_TransportFailureIsServiceUnavailable(t *testing.T) {
	p := &fakeProducer{err: errors.New("boom")}
	c := NewClient(p)

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("ServiceUnavailable result should carry an error message")
	}
}

func TestClient_PartitionKeyFromHeaderWhenConfigured(t *testing.T) {
	p := &fakeProducer{}
	c := NewClient(p)
	c.PartitionKeyHeader = "x-tenant"

	c.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"x-tenant": "acme"}, []byte("{}"))

	if !p.partKeySet {
		t.Fatal("partition key should be set when the configured header is present")
	}
	if p.partKeyValue != "acme" {
		t.Errorf("partition key = %q, want %q", p.partKeyValue, "acme")
	}
	// The header is still carried as an event property (matching the .NET converter).
	if got := p.lastProps["x-tenant"]; got != "acme" {
		t.Errorf(`properties["x-tenant"] = %v, want "acme"`, got)
	}
}

func TestClient_PartitionKeyOmittedWhenHeaderAbsentOrUnconfigured(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		headers map[string]string
	}{
		{name: "unconfigured", header: "", headers: map[string]string{"x-tenant": "acme"}},
		{name: "configured but header absent", header: "x-tenant", headers: map[string]string{"other": "v"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakeProducer{}
			c := NewClient(p)
			c.PartitionKeyHeader = tt.header

			c.Send(context.Background(), benzene.NewTopic("greet"), tt.headers, []byte("{}"))

			if p.partKeySet {
				t.Errorf("partition key = %q, want nil", p.partKeyValue)
			}
		})
	}
}

func TestClient_ContextIsForwardedToProducer(t *testing.T) {
	p := &fakeProducer{}
	c := NewClient(p)

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "value")
	c.Send(ctx, benzene.NewTopic("greet"), nil, []byte("{}"))

	if p.lastCtx.Value(ctxKey{}) != "value" {
		t.Error("Send should forward the caller's context to the producer")
	}
}
