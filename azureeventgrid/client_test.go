package azureeventgrid

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/messaging"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid"

	benzene "github.com/daniellepelley/benzene-go"
)

type fakePublishAPI struct {
	events []messaging.CloudEvent
	err    error
}

func (f *fakePublishAPI) PublishCloudEvents(_ context.Context, events []messaging.CloudEvent, _ *azeventgrid.PublishCloudEventsOptions) (azeventgrid.PublishCloudEventsResponse, error) {
	f.events = append(f.events, events...)
	if f.err != nil {
		return azeventgrid.PublishCloudEventsResponse{}, f.err
	}
	return azeventgrid.PublishCloudEventsResponse{}, nil
}

func TestClient_SendPublishesCloudEvent(t *testing.T) {
	api := &fakePublishAPI{}
	client := NewClient(api, "com.example.orders")

	result := client.Send(context.Background(), benzene.NewTopic("order:created"),
		map[string]string{"X-Correlation-Id": "abc"}, []byte(`{"id":42}`))

	if result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if len(api.events) != 1 {
		t.Fatalf("published %d events, want 1", len(api.events))
	}
	event := api.events[0]

	if event.Source != "com.example.orders" {
		t.Errorf("Source = %q, want the client's source", event.Source)
	}
	if event.Type != "order:created" {
		t.Errorf("Type = %q, want the topic", event.Type)
	}
	if event.DataContentType == nil || *event.DataContentType != "application/json" {
		t.Errorf("DataContentType = %v, want application/json", event.DataContentType)
	}

	// Data is carried as raw JSON (json.RawMessage), so it lands as the CloudEvent "data"
	// attribute verbatim rather than base64-encoded into "data_base64".
	raw, ok := event.Data.(json.RawMessage)
	if !ok {
		t.Fatalf("Data is %T, want json.RawMessage (else the SDK base64-encodes it)", event.Data)
	}
	if string(raw) != `{"id":42}` {
		t.Errorf("Data = %s, want the message verbatim", raw)
	}

	// Header travels as a CloudEvent extension attribute under its lower-cased name.
	if got := event.Extensions["x-correlation-id"]; got != "abc" {
		t.Errorf("Extensions[x-correlation-id] = %v, want abc (lower-cased header)", got)
	}
	if _, present := event.Extensions["X-Correlation-Id"]; present {
		t.Error("extension attribute should be lower-cased, not the original mixed-case key")
	}
}

func TestClient_SendMarshalsToJSONData(t *testing.T) {
	// End-to-end proof the raw-JSON data survives the SDK's own marshaling (data, not
	// data_base64) - the distinction that json.RawMessage vs a plain []byte controls.
	api := &fakePublishAPI{}
	client := NewClient(api, "s")

	if result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`)); result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}

	encoded, err := json.Marshal(api.events[0])
	if err != nil {
		t.Fatalf("marshaling the CloudEvent failed: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("CloudEvent is not a JSON object: %v", err)
	}
	if _, base64Encoded := wire["data_base64"]; base64Encoded {
		t.Error("data was base64-encoded; want inline JSON under \"data\"")
	}
	if string(wire["data"]) != `{"name":"World"}` {
		t.Errorf("data = %s, want the message inlined as JSON", wire["data"])
	}
}

func TestClient_SendWithoutHeadersSetsNoExtensions(t *testing.T) {
	api := &fakePublishAPI{}
	client := NewClient(api, "s")

	if result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`)); result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if api.events[0].Extensions != nil {
		t.Errorf("Extensions = %v, want nil when there are no headers", api.events[0].Extensions)
	}
}

func TestClient_TransportErrorIsServiceUnavailable(t *testing.T) {
	api := &fakePublishAPI{err: errors.New("event grid unreachable")}
	client := NewClient(api, "s")

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))
	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("Errors should carry the transport failure detail")
	}
}

func TestClient_EmptySourceIsServiceUnavailable(t *testing.T) {
	// Event Grid requires a non-empty CloudEvent source; NewCloudEvent rejects an empty one, so
	// a misconfigured client fails to service-unavailable rather than publishing (or panicking).
	api := &fakePublishAPI{}
	client := NewClient(api, "")

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))
	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(api.events) != 0 {
		t.Error("nothing should be published when the event cannot be built")
	}
}

func TestClient_EmptyBodyPublishesValidNullData(t *testing.T) {
	// An empty Benzene body must not produce a CloudEvent that fails to serialize: a json.RawMessage
	// over an empty non-nil slice marshals to nothing (invalid JSON). The client normalizes it to a
	// valid "data": null so the event round-trips through the SDK's own marshaler.
	for _, body := range [][]byte{[]byte(""), nil} {
		api := &fakePublishAPI{}
		client := NewClient(api, "s")
		if result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, body); result.Status != benzene.StatusAccepted {
			t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
		}
		encoded, err := json.Marshal(api.events[0])
		if err != nil {
			t.Fatalf("empty body %q: CloudEvent failed to marshal: %v", body, err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatalf("empty body %q: CloudEvent is not a JSON object: %v", body, err)
		}
		if string(wire["data"]) != "null" {
			t.Errorf("empty body %q: data = %s, want null", body, wire["data"])
		}
	}
}

func TestNewClientSetsFields(t *testing.T) {
	api := &fakePublishAPI{}
	client := NewClient(api, "src")
	if client.API != api || client.Source != "src" {
		t.Errorf("NewClient did not set fields: %+v", client)
	}
}
