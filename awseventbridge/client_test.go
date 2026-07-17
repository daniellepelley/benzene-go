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
	"github.com/daniellepelley/benzene-go/wire"
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

func TestClient_SendPublishesEnvelopeDetail(t *testing.T) {
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

	var envelopeReq wire.Request
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &envelopeReq); err != nil {
		t.Fatalf("Detail is not a wire envelope: %v; detail = %s", err, aws.ToString(entry.Detail))
	}
	if envelopeReq.Topic != "greet" || envelopeReq.Body != `{"name":"World"}` || envelopeReq.Headers["x-correlation-id"] != "abc" {
		t.Errorf("envelope = %+v, want topic/headers/body carried", envelopeReq)
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
