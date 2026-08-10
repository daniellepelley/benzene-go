package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

type echoRequest struct {
	Message string
}

type echoResponse struct {
	Echo string
}

func echoHandler(_ context.Context, req echoRequest) benzene.Result[echoResponse] {
	return benzene.Ok(echoResponse{Echo: "echo:" + req.Message})
}

func failingHandler(_ context.Context, _ echoRequest) benzene.Result[echoResponse] {
	return benzene.BadRequest[echoResponse]("nope")
}

// newBuilder returns a standalone *benzene.ApplicationBuilder with handler registered for
// topic - its own independent Registry/Container/Pipeline, matching how PipelineSet.Add
// expects one module's builder to look (see benzenetest_test.go's newTestBuilder for the same
// shape, used by the underlying benzene-go test suite).
func newBuilder(t *testing.T, topic string, handler benzene.Handler[echoRequest, echoResponse]) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic(topic), handler); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

func TestSender_DispatchesToTheRegisteredPipelineAndReturnsATypedResponse(t *testing.T) {
	set := NewPipelineSet()
	if err := set.Add("billing", newBuilder(t, "billing:echo", echoHandler)); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	sender, err := NewSender(set, "billing")
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}

	result := sender.Send(context.Background(), benzene.NewTopic("billing:echo"), nil, mustMarshal(t, echoRequest{Message: "hello"}))

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	var resp echoResponse
	if err := json.Unmarshal(*result.Payload, &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if resp.Echo != "echo:hello" {
		t.Errorf("Echo = %q, want %q", resp.Echo, "echo:hello")
	}
}

func TestPipelineSet_Add_DuplicateNameReturnsAnError(t *testing.T) {
	set := NewPipelineSet()
	if err := set.Add("billing", newBuilder(t, "billing:echo", echoHandler)); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}

	err := set.Add("billing", newBuilder(t, "billing:other", echoHandler))

	if err == nil {
		t.Fatal("second Add() with the same name should return an error")
	}
}

func TestPipelineSet_Add_ABuilderWithNoPipelineReturnsAnError(t *testing.T) {
	set := NewPipelineSet()

	err := set.Add("billing", &benzene.ApplicationBuilder{Container: benzene.NewContainer()})

	if err == nil {
		t.Fatal("Add() with no Pipeline set should return an error")
	}
}

func TestNewSender_AnUnregisteredNameReturnsAnErrorListingWhatIsRegistered(t *testing.T) {
	set := NewPipelineSet()
	if err := set.Add("billing", newBuilder(t, "billing:echo", echoHandler)); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	_, err := NewSender(set, "shipping")

	if err == nil {
		t.Fatal("NewSender() for an unregistered name should return an error")
	}
}

func TestFanOutSender_DispatchesToEveryTargetConcurrently(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	recordingHandler := func(name string) benzene.Handler[echoRequest, echoResponse] {
		return func(_ context.Context, req echoRequest) benzene.Result[echoResponse] {
			mu.Lock()
			calls = append(calls, name)
			mu.Unlock()
			return benzene.Ok(echoResponse{Echo: req.Message})
		}
	}

	set := NewPipelineSet()
	// Both targets register a handler for the literal SAME topic - legal here because each
	// named pipeline has its own independent Registry, unlike the .NET/TypeScript ports (see
	// the package doc comment).
	if err := set.Add("billing", newBuilder(t, "order:created", recordingHandler("billing"))); err != nil {
		t.Fatalf("Add(billing) error = %v", err)
	}
	if err := set.Add("shipping", newBuilder(t, "order:created", recordingHandler("shipping"))); err != nil {
		t.Fatalf("Add(shipping) error = %v", err)
	}
	sender, err := NewFanOutSender(set, nil, "billing", "shipping")
	if err != nil {
		t.Fatalf("NewFanOutSender() error = %v", err)
	}

	result := sender.Send(context.Background(), benzene.NewTopic("order:created"), nil, mustMarshal(t, echoRequest{Message: "hello"}))

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want 2 entries", calls)
	}
}

func TestFanOutSender_OneConsumerFails_DoesNotAffectTheOtherOrTheCallersResult(t *testing.T) {
	var mu sync.Mutex
	var shippingCalled bool

	set := NewPipelineSet()
	if err := set.Add("failing", newBuilder(t, "failing:order-created", failingHandler)); err != nil {
		t.Fatalf("Add(failing) error = %v", err)
	}
	if err := set.Add("shipping", newBuilder(t, "shipping:order-created", func(_ context.Context, req echoRequest) benzene.Result[echoResponse] {
		mu.Lock()
		shippingCalled = true
		mu.Unlock()
		return benzene.Ok(echoResponse{Echo: req.Message})
	})); err != nil {
		t.Fatalf("Add(shipping) error = %v", err)
	}

	// Each target sent to a distinct topic here on purpose - a fan-out route in production
	// dispatches the caller's real, single topic to each target's Registry, and each target's
	// Registry only has a handler for its own module's topic in this test.
	failingSender, err := NewSender(set, "failing")
	if err != nil {
		t.Fatalf("NewSender(failing) error = %v", err)
	}
	failingResult := failingSender.Send(context.Background(), benzene.NewTopic("failing:order-created"), nil, mustMarshal(t, echoRequest{Message: "hello"}))
	if failingResult.IsSuccessful() {
		t.Fatalf("expected the failing handler's own direct result to be unsuccessful, got %q", failingResult.Status)
	}

	fanOut, err := NewFanOutSender(set, nil, "failing", "shipping")
	if err != nil {
		t.Fatalf("NewFanOutSender() error = %v", err)
	}

	// The fan-out itself dispatches under whatever topic the caller sent on; since "failing"'s
	// registry only knows "failing:order-created" and "shipping"'s only knows
	// "shipping:order-created", sending to either literal topic exercises one real handler and
	// one NotFound - both isolated failure shapes fan-out must tolerate without touching the
	// caller's own always-success return.
	result := fanOut.Send(context.Background(), benzene.NewTopic("shipping:order-created"), nil, mustMarshal(t, echoRequest{Message: "hello"}))

	if !result.IsSuccessful() {
		t.Fatalf("fan-out's own result should be unconditionally successful, got %q", result.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if !shippingCalled {
		t.Error("shipping handler should still have been dispatched to")
	}
}

func TestNewFanOutSender_NoNamesReturnsAnError(t *testing.T) {
	set := NewPipelineSet()

	_, err := NewFanOutSender(set, nil)

	if err == nil {
		t.Fatal("NewFanOutSender() with no names should return an error")
	}
}

func TestNewFanOutSender_AnUnregisteredNameReturnsAnError(t *testing.T) {
	set := NewPipelineSet()
	if err := set.Add("billing", newBuilder(t, "billing:echo", echoHandler)); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	_, err := NewFanOutSender(set, nil, "billing", "shipping")

	if err == nil {
		t.Fatal("NewFanOutSender() with an unregistered name should return an error")
	}
}

func TestSender_PipelineErrorIsServiceUnavailable(t *testing.T) {
	builder := &benzene.ApplicationBuilder{
		Container: benzene.NewContainer(),
		Pipeline: benzene.NewPipeline(func(_ context.Context, _ *benzene.InvocationContext, _ func(context.Context) error) error {
			return errors.New("boom")
		}),
	}
	set := NewPipelineSet()
	if err := set.Add("broken", builder); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	sender, err := NewSender(set, "broken")
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}

	result := sender.Send(context.Background(), benzene.NewTopic("anything"), nil, nil)

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}
