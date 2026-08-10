package awsstepfunctions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sfn/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

type fakeStartExecutionAPI struct {
	input   *sfn.StartExecutionInput
	err     error
	calls   int
	lastCtx context.Context
}

func (f *fakeStartExecutionAPI) StartExecution(ctx context.Context, params *sfn.StartExecutionInput, _ ...func(*sfn.Options)) (*sfn.StartExecutionOutput, error) {
	f.calls++
	f.input = params
	f.lastCtx = ctx
	if f.err != nil {
		return nil, f.err
	}
	return &sfn.StartExecutionOutput{}, nil
}

func TestClient_SuccessfulStartReturnsAccepted(t *testing.T) {
	api := &fakeStartExecutionAPI{}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")

	result := c.Send(context.Background(), benzene.NewTopic("order:create"), nil, []byte(`{"id":1}`))

	if result.Status != benzene.StatusAccepted {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusAccepted)
	}
	if api.calls != 1 {
		t.Fatalf("calls = %d, want 1", api.calls)
	}
	if *api.input.StateMachineArn != "arn:aws:states:::stateMachine:demo" {
		t.Errorf("StateMachineArn = %q, want the configured ARN", *api.input.StateMachineArn)
	}
}

func TestClient_InputCarriesEnvelope(t *testing.T) {
	api := &fakeStartExecutionAPI{}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")

	c.Send(context.Background(), benzene.NewTopic("order:create"), map[string]string{"x-correlation-id": "abc"}, []byte(`{"id":1}`))

	if api.input.Input == nil {
		t.Fatal("StartExecutionInput.Input is nil")
	}
	req, err := wire.UnmarshalRequest([]byte(*api.input.Input))
	if err != nil {
		t.Fatalf("Input is not a wire envelope: %v", err)
	}
	if req.Topic != "order:create" {
		t.Errorf("envelope topic = %q, want %q", req.Topic, "order:create")
	}
	if req.Headers["x-correlation-id"] != "abc" {
		t.Errorf("envelope header = %q, want %q", req.Headers["x-correlation-id"], "abc")
	}
	if req.Body != `{"id":1}` {
		t.Errorf("envelope body = %q, want %q", req.Body, `{"id":1}`)
	}
}

func TestClient_TransportFailureIsServiceUnavailable(t *testing.T) {
	api := &fakeStartExecutionAPI{err: errors.New("boom")}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")

	result := c.Send(context.Background(), benzene.NewTopic("order:create"), nil, []byte("{}"))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 {
		t.Error("ServiceUnavailable result should carry an error message")
	}
}

func TestClient_ExecutionAlreadyExistsIsIdempotentAccepted(t *testing.T) {
	// Starting with an execution name that already exists is an idempotent retry, not a failure -
	// it maps to Accepted (matching .NET's catch of ExecutionAlreadyExists), not ServiceUnavailable.
	api := &fakeStartExecutionAPI{err: &types.ExecutionAlreadyExists{Message: aws.String("exists")}}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")
	c.ExecutionName = func(benzene.Topic, []byte) string { return "order-1" }

	result := c.Send(context.Background(), benzene.NewTopic("order:create"), nil, []byte("{}"))

	if result.Status != benzene.StatusAccepted {
		t.Errorf("Status = %q, want %q (ExecutionAlreadyExists is an idempotent no-op)", result.Status, benzene.StatusAccepted)
	}
}

func TestClient_ExecutionNameOmittedWhenNotConfigured(t *testing.T) {
	api := &fakeStartExecutionAPI{}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")

	c.Send(context.Background(), benzene.NewTopic("order:create"), nil, []byte("{}"))

	if api.input.Name != nil {
		t.Errorf("Name = %v, want nil so AWS generates a UUID", api.input.Name)
	}
}

func TestClient_ExecutionNameIsSetAndSanitized(t *testing.T) {
	api := &fakeStartExecutionAPI{}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")
	c.ExecutionName = func(topic benzene.Topic, message []byte) string {
		return "order/create id=1 <weird>\t\x01done"
	}

	c.Send(context.Background(), benzene.NewTopic("order:create"), nil, []byte("{}"))

	if api.input.Name == nil {
		t.Fatal("Name is nil, want a sanitized execution name")
	}
	got := *api.input.Name
	want := "order-create-id=1--weird---done"
	if got != want {
		t.Errorf("sanitized Name = %q, want %q", got, want)
	}
	for _, r := range disallowedExecutionNameChars {
		if strings.ContainsRune(got, r) {
			t.Errorf("sanitized Name %q still contains a disallowed char %q", got, string(r))
		}
	}
}

func TestClient_ExecutionNameIsCappedAt80Chars(t *testing.T) {
	api := &fakeStartExecutionAPI{}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")
	long := strings.Repeat("a", 200)
	c.ExecutionName = func(topic benzene.Topic, message []byte) string { return long }

	c.Send(context.Background(), benzene.NewTopic("order:create"), nil, []byte("{}"))

	if got := len(*api.input.Name); got != 80 {
		t.Errorf("len(Name) = %d, want 80", got)
	}
}

func TestClient_ExecutionNameEmptyStringStaysEmpty(t *testing.T) {
	api := &fakeStartExecutionAPI{}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")
	c.ExecutionName = func(topic benzene.Topic, message []byte) string { return "" }

	c.Send(context.Background(), benzene.NewTopic("order:create"), nil, []byte("{}"))

	// An empty derived name is passed through as an empty Name (AWS then generates a UUID) - the
	// sanitizer must not panic or alter it.
	if api.input.Name == nil || *api.input.Name != "" {
		t.Errorf("Name = %v, want an empty string", api.input.Name)
	}
}

func TestClient_ContextIsForwardedToAPI(t *testing.T) {
	api := &fakeStartExecutionAPI{}
	c := NewClient(api, "arn:aws:states:::stateMachine:demo")

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "value")
	c.Send(ctx, benzene.NewTopic("order:create"), nil, []byte("{}"))

	if api.lastCtx.Value(ctxKey{}) != "value" {
		t.Error("Send should forward the caller's context to the API call")
	}
}
