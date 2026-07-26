package awseventbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	benzene "github.com/daniellepelley/benzene-go"
)

type fakePutEventsAPI struct {
	inputs []*eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
	err    error
}

func (f *fakePutEventsAPI) PutEvents(_ context.Context, params *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.inputs = append(f.inputs, params)
	if f.err != nil {
		return nil, f.err
	}
	if f.output != nil {
		return f.output, nil
	}
	return &eventbridge.PutEventsOutput{}, nil
}

func TestClient_SendEmbedsHeadersInDetail(t *testing.T) {
	api := &fakePutEventsAPI{}
	client := NewClient(api, "com.example.orders")
	client.EventBusName = "orders-bus"

	result := client.Send(context.Background(), benzene.NewTopic("greet"),
		map[string]string{"x-correlation-id": "abc"}, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if len(api.inputs) != 1 || len(api.inputs[0].Entries) != 1 {
		t.Fatalf("PutEvents inputs = %v, want one call with one entry", api.inputs)
	}
	entry := api.inputs[0].Entries[0]
	if aws.ToString(entry.Source) != "com.example.orders" {
		t.Errorf("Source = %q, want the client's source", aws.ToString(entry.Source))
	}
	if aws.ToString(entry.DetailType) != "greet" {
		t.Errorf("DetailType = %q, want the topic", aws.ToString(entry.DetailType))
	}
	if aws.ToString(entry.EventBusName) != "orders-bus" {
		t.Errorf("EventBusName = %q, want orders-bus", aws.ToString(entry.EventBusName))
	}

	var detail map[string]json.RawMessage
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatalf("Detail is not a JSON object: %v; detail = %s", err, aws.ToString(entry.Detail))
	}
	if string(detail["name"]) != `"World"` {
		t.Errorf(`detail["name"] = %s, want the original message field preserved`, detail["name"])
	}
	var embedded map[string]string
	if err := json.Unmarshal(detail[EmbeddedHeadersKey], &embedded); err != nil {
		t.Fatalf("%s is not a header object: %v", EmbeddedHeadersKey, err)
	}
	if embedded["x-correlation-id"] != "abc" {
		t.Errorf("embedded headers = %v, want x-correlation-id=abc", embedded)
	}
}

func TestClient_SendWithNoHeadersLeavesMessageUnmodified(t *testing.T) {
	api := &fakePutEventsAPI{}
	client := NewClient(api, "s")

	if result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`)); result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if got := aws.ToString(api.inputs[0].Entries[0].Detail); got != `{"name":"World"}` {
		t.Errorf("Detail = %q, want the message verbatim when there are no headers to embed", got)
	}
}

func TestClient_SendWithNonObjectMessageDropsHeaders(t *testing.T) {
	// Matches the reference implementation: embedding only happens when the message parses
	// as a JSON object - there's nowhere for a header object to live inside a scalar/array.
	api := &fakePutEventsAPI{}
	client := NewClient(api, "s")

	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"x-correlation-id": "abc"}, []byte(`"plain string payload"`))
	if result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if got := aws.ToString(api.inputs[0].Entries[0].Detail); got != `"plain string payload"` {
		t.Errorf("Detail = %q, want the message verbatim (headers dropped, not the whole send failed)", got)
	}
}

func TestClient_DefaultBusOmitsEventBusName(t *testing.T) {
	api := &fakePutEventsAPI{}
	client := NewClient(api, "com.example.orders")

	if result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`)); result.Status != benzene.StatusAccepted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if api.inputs[0].Entries[0].EventBusName != nil {
		t.Errorf("EventBusName = %q, want nil for the default bus", aws.ToString(api.inputs[0].Entries[0].EventBusName))
	}
}

func TestClient_TransportErrorIsServiceUnavailable(t *testing.T) {
	api := &fakePutEventsAPI{err: errors.New("throttled")}
	client := NewClient(api, "s")

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))
	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

func TestEmbedDetailHeaders(t *testing.T) {
	tests := []struct {
		name    string
		message string
		headers map[string]string
		want    string
	}{
		{name: "no headers returns message verbatim", message: `{"a":1}`, headers: nil, want: `{"a":1}`},
		{name: "empty headers map returns message verbatim", message: `{"a":1}`, headers: map[string]string{}, want: `{"a":1}`},
		{name: "non-object message drops headers", message: `[1,2,3]`, headers: map[string]string{"h": "v"}, want: `[1,2,3]`},
		{name: "malformed message drops headers", message: `not json`, headers: map[string]string{"h": "v"}, want: `not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := embedDetailHeaders([]byte(tt.message), tt.headers); got != tt.want {
				t.Errorf("embedDetailHeaders() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("object message embeds headers under EmbeddedHeadersKey", func(t *testing.T) {
		got := embedDetailHeaders([]byte(`{"a":1}`), map[string]string{"h": "v"})
		var detail map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got), &detail); err != nil {
			t.Fatalf("result is not a JSON object: %v; got = %s", err, got)
		}
		if string(detail["a"]) != "1" {
			t.Errorf(`detail["a"] = %s, want the original field preserved`, detail["a"])
		}
		var embedded map[string]string
		if err := json.Unmarshal(detail[EmbeddedHeadersKey], &embedded); err != nil || embedded["h"] != "v" {
			t.Errorf("embedded headers = %v (err=%v), want h=v", embedded, err)
		}
	})
}

func TestClient_FailedEntryIsServiceUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		output *eventbridge.PutEventsOutput
	}{
		{
			name: "with per-entry error message",
			output: &eventbridge.PutEventsOutput{
				FailedEntryCount: 1,
				Entries:          []types.PutEventsResultEntry{{ErrorCode: aws.String("ThrottlingException"), ErrorMessage: aws.String("slow down")}},
			},
		},
		{
			name:   "without entry detail",
			output: &eventbridge.PutEventsOutput{FailedEntryCount: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakePutEventsAPI{output: tt.output}
			client := NewClient(api, "s")

			result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))
			if result.Status != benzene.StatusServiceUnavailable {
				t.Errorf("Status = %q, want %q (FailedEntryCount reports failures without a Go error)", result.Status, benzene.StatusServiceUnavailable)
			}
			if len(result.Errors) == 0 {
				t.Error("Errors should carry the failure detail")
			}
		})
	}
}
