package azurequeuestorage

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

type fakeEnqueueAPI struct {
	contents []string
	err      error
}

func (f *fakeEnqueueAPI) EnqueueMessage(_ context.Context, content string, _ *azqueue.EnqueueMessageOptions) (azqueue.EnqueueMessagesResponse, error) {
	f.contents = append(f.contents, content)
	if f.err != nil {
		return azqueue.EnqueueMessagesResponse{}, f.err
	}
	return azqueue.EnqueueMessagesResponse{}, nil
}

func TestClient_SendEnqueuesEnvelope(t *testing.T) {
	api := &fakeEnqueueAPI{}
	client := NewClient(api)

	result := client.Send(context.Background(), benzene.NewTopic("order:create"),
		map[string]string{"x-correlation-id": "abc"}, []byte(`{"id":42}`))

	if result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if len(api.contents) != 1 {
		t.Fatalf("enqueued %d messages, want 1", len(api.contents))
	}

	// The whole {topic, headers, body} envelope is the message text (Queue Storage has no
	// attributes bag) - and it round-trips through the same wire.Request the inbound side reads.
	got, err := wire.UnmarshalRequest([]byte(api.contents[0]))
	if err != nil {
		t.Fatalf("enqueued content is not a wire.Request envelope: %v; content = %s", err, api.contents[0])
	}
	if got.Topic != "order:create" {
		t.Errorf("envelope Topic = %q, want the topic", got.Topic)
	}
	if got.Headers["x-correlation-id"] != "abc" {
		t.Errorf("envelope Headers = %v, want x-correlation-id=abc", got.Headers)
	}
	if got.Body != `{"id":42}` {
		t.Errorf("envelope Body = %q, want the message verbatim", got.Body)
	}
}

func TestClient_SendVerbatimNoBase64(t *testing.T) {
	// azqueue enqueues the content string as-is (no base64), so the stored message text starts
	// with the envelope's JSON `{` - a consumer must not base64-decode it.
	api := &fakeEnqueueAPI{}
	client := NewClient(api)

	if result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`)); result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if got := api.contents[0]; len(got) == 0 || got[0] != '{' {
		t.Errorf("enqueued content = %q, want raw envelope JSON (not base64-encoded)", got)
	}
}

func TestClient_TransportErrorIsServiceUnavailable(t *testing.T) {
	api := &fakeEnqueueAPI{err: errors.New("queue unreachable")}
	client := NewClient(api)

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))
	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("Errors should carry the transport failure detail")
	}
}

func TestNewClientSetsField(t *testing.T) {
	api := &fakeEnqueueAPI{}
	if client := NewClient(api); client.API != api {
		t.Errorf("NewClient did not set API: %+v", client)
	}
}
