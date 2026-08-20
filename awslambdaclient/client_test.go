package awslambdaclient

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

type fakeInvokeAPI struct {
	input   *lambda.InvokeInput
	output  *lambda.InvokeOutput
	err     error
	calls   int
	lastCtx context.Context
}

func (f *fakeInvokeAPI) Invoke(ctx context.Context, params *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	f.calls++
	f.input = params
	f.lastCtx = ctx
	if f.err != nil {
		return nil, f.err
	}
	if f.output != nil {
		return f.output, nil
	}
	return &lambda.InvokeOutput{}, nil
}

// responsePayload marshals a wire.Response into the JSON bytes a target Lambda would return.
func responsePayload(t *testing.T, resp wire.Response) []byte {
	t.Helper()
	b, err := wire.MarshalResponse(resp)
	if err != nil {
		t.Fatalf("MarshalResponse: %v", err)
	}
	return b
}

func TestNewClient_DefaultsToRequestResponse(t *testing.T) {
	c := NewClient(&fakeInvokeAPI{}, "target-fn")
	if c.InvocationType != types.InvocationTypeRequestResponse {
		t.Errorf("InvocationType = %q, want %q", c.InvocationType, types.InvocationTypeRequestResponse)
	}
	if c.FunctionName != "target-fn" {
		t.Errorf("FunctionName = %q, want %q", c.FunctionName, "target-fn")
	}
}

func TestClient_RequestResponseReturnsTargetResult(t *testing.T) {
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{
		Payload: responsePayload(t, wire.Response{StatusCode: "ok", Body: `{"greeting":"hi"}`}),
	}}
	c := NewClient(api, "target-fn")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusOk {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload == nil || string(*result.Payload) != `{"greeting":"hi"}` {
		t.Errorf("Payload = %v, want the target's body", result.Payload)
	}
	if api.calls != 1 {
		t.Fatalf("calls = %d, want 1", api.calls)
	}
	if *api.input.FunctionName != "target-fn" {
		t.Errorf("FunctionName = %q, want %q", *api.input.FunctionName, "target-fn")
	}
	if api.input.InvocationType != types.InvocationTypeRequestResponse {
		t.Errorf("InvocationType = %q, want RequestResponse", api.input.InvocationType)
	}
}

func TestClient_SuccessWithEmptyBodyHasNoPayload(t *testing.T) {
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{
		Payload: responsePayload(t, wire.Response{StatusCode: "ok", Body: ""}),
	}}
	c := NewClient(api, "target-fn")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusOk {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload != nil {
		t.Errorf("Payload = %v, want nil for an empty body", result.Payload)
	}
}

func TestClient_FailureStatusCarriesErrorDetail(t *testing.T) {
	ep, err := wire.MarshalErrorPayload(wire.NewErrorPayload("not-found", []string{"no such thing"}))
	if err != nil {
		t.Fatalf("MarshalErrorPayload: %v", err)
	}
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{
		Payload: responsePayload(t, wire.Response{StatusCode: "not-found", Body: string(ep)}),
	}}
	c := NewClient(api, "target-fn")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusNotFound {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusNotFound)
	}
	if len(result.Errors) == 0 || result.Errors[0].Message != "no such thing" {
		t.Errorf("Errors = %v, want the error detail", result.Errors)
	}
}

func TestClient_FailureStatusWithoutDetailHasNoErrors(t *testing.T) {
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{
		Payload: responsePayload(t, wire.Response{StatusCode: "not-found", Body: ""}),
	}}
	c := NewClient(api, "target-fn")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusNotFound {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusNotFound)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v, want none for a detail-less failure", result.Errors)
	}
}

func TestClient_EventInvocationReturnsAcceptedWithoutParsing(t *testing.T) {
	// Payload is deliberately non-envelope garbage: an Event invoke must not parse it.
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{Payload: []byte("not-json")}}
	c := NewClient(api, "target-fn")
	c.InvocationType = types.InvocationTypeEvent

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusAccepted {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if api.input.InvocationType != types.InvocationTypeEvent {
		t.Errorf("InvocationType = %q, want Event", api.input.InvocationType)
	}
}

func TestClient_FunctionErrorIsUnexpectedError(t *testing.T) {
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{
		FunctionError: aws.String("Unhandled"),
		Payload:       []byte(`{"errorMessage":"boom"}`),
	}}
	c := NewClient(api, "target-fn")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusUnexpectedError {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusUnexpectedError)
	}
	if len(result.Errors) == 0 {
		t.Error("a function error should carry an error message")
	}
}

func TestClient_TransportFailureIsServiceUnavailable(t *testing.T) {
	api := &fakeInvokeAPI{err: errors.New("boom")}
	c := NewClient(api, "target-fn")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("ServiceUnavailable result should carry an error message")
	}
}

func TestClient_UnparseablePayloadIsServiceUnavailable(t *testing.T) {
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{Payload: []byte("not-json")}}
	c := NewClient(api, "target-fn")

	result := c.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte("{}"))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("a parse failure should carry an error message")
	}
}

func TestClient_PayloadCarriesEnvelope(t *testing.T) {
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{
		Payload: responsePayload(t, wire.Response{StatusCode: "ok", Body: "{}"}),
	}}
	c := NewClient(api, "target-fn")

	c.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"x-correlation-id": "abc"}, []byte(`{"name":"World"}`))

	req, err := wire.UnmarshalRequest(api.input.Payload)
	if err != nil {
		t.Fatalf("invoke payload is not a wire envelope: %v", err)
	}
	if req.Topic != "greet" {
		t.Errorf("envelope topic = %q, want %q", req.Topic, "greet")
	}
	if req.Headers["x-correlation-id"] != "abc" {
		t.Errorf("envelope header = %q, want %q", req.Headers["x-correlation-id"], "abc")
	}
	if req.Body != `{"name":"World"}` {
		t.Errorf("envelope body = %q, want %q", req.Body, `{"name":"World"}`)
	}
}

func TestClient_ContextIsForwardedToAPI(t *testing.T) {
	api := &fakeInvokeAPI{output: &lambda.InvokeOutput{
		Payload: responsePayload(t, wire.Response{StatusCode: "ok", Body: "{}"}),
	}}
	c := NewClient(api, "target-fn")

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "value")
	c.Send(ctx, benzene.NewTopic("greet"), nil, []byte("{}"))

	if api.lastCtx.Value(ctxKey{}) != "value" {
		t.Error("Send should forward the caller's context to the API call")
	}
}
