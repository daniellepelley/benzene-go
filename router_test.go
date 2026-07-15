package benzene

import (
	"context"
	"strings"
	"testing"
)

func TestRouterMiddleware_DispatchesToRegisteredHandler(t *testing.T) {
	registry := NewRegistry()
	topic := NewTopic("hello:world")
	if err := Register(registry, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	pipeline := NewPipeline(RouterMiddleware(registry))
	ic := NewInvocationContext(topic, nil, helloRequest{Name: "World"}, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if ic.Result == nil {
		t.Fatal("Result should be populated after RouterMiddleware runs")
	}
	if ic.Result.ResultStatus() != StatusOk {
		t.Errorf("ResultStatus() = %q, want %q", ic.Result.ResultStatus(), StatusOk)
	}
	payload, ok := ic.Result.ResultPayload().(helloResponse)
	if !ok || payload.Message != "Hello World" {
		t.Errorf("ResultPayload() = %v, want {Message: Hello World}", ic.Result.ResultPayload())
	}
}

func TestRouterMiddleware_MissingHandlerIsNotFound(t *testing.T) {
	registry := NewRegistry()
	pipeline := NewPipeline(RouterMiddleware(registry))
	ic := NewInvocationContext(NewTopic("no:such:topic"), nil, nil, nil)

	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v, want nil (missing handler is a Result, not a Go error)", err)
	}
	if ic.Result.ResultStatus() != StatusNotFound {
		t.Errorf("ResultStatus() = %q, want %q", ic.Result.ResultStatus(), StatusNotFound)
	}
	if ic.Result.ResultPayload() != nil {
		t.Errorf("ResultPayload() = %v, want nil for NotFound", ic.Result.ResultPayload())
	}
}

func TestRouterMiddleware_RequestConversionFailureIsBadRequest(t *testing.T) {
	registry := NewRegistry()
	topic := NewTopic("hello:world")
	if err := Register(registry, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	pipeline := NewPipeline(RouterMiddleware(registry))
	// A request payload that is neither a helloRequest nor []byte/json.RawMessage cannot
	// be converted, per convertRequest's rules.
	ic := NewInvocationContext(topic, nil, 12345, nil)

	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if ic.Result.ResultStatus() != StatusBadRequest {
		t.Errorf("ResultStatus() = %q, want %q", ic.Result.ResultStatus(), StatusBadRequest)
	}
	if len(ic.Result.ResultErrors()) == 0 {
		t.Error("BadRequest result should carry the conversion error message")
	}
}

func TestRouterMiddleware_HandlerPanicIsServiceUnavailable(t *testing.T) {
	registry := NewRegistry()
	topic := NewTopic("panics")
	panicking := Handler[helloRequest, helloResponse](func(_ context.Context, _ helloRequest) Result[helloResponse] {
		panic("boom")
	})
	if err := Register(registry, topic, panicking); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	pipeline := NewPipeline(RouterMiddleware(registry))
	ic := NewInvocationContext(topic, nil, helloRequest{Name: "x"}, nil)

	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v, want nil (a handler panic must not crash the pipeline)", err)
	}
	if ic.Result.ResultStatus() != StatusServiceUnavailable {
		t.Errorf("ResultStatus() = %q, want %q", ic.Result.ResultStatus(), StatusServiceUnavailable)
	}
	if len(ic.Result.ResultErrors()) == 0 || !strings.Contains(ic.Result.ResultErrors()[0], "boom") {
		t.Errorf("ResultErrors() = %v, want it to mention the panic value", ic.Result.ResultErrors())
	}
}

func TestRouterMiddleware_IsAnOrdinaryMiddlewareThatCallsNext(t *testing.T) {
	registry := NewRegistry()
	topic := NewTopic("hello:world")
	if err := Register(registry, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	var ranAfterRouter bool
	afterRouter := func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
		ranAfterRouter = true
		return next(ctx)
	}

	pipeline := NewPipeline(RouterMiddleware(registry), afterRouter)
	ic := NewInvocationContext(topic, nil, helloRequest{Name: "World"}, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !ranAfterRouter {
		t.Error("middleware registered after the router should still run - the router is an ordinary middleware, not a hard pipeline terminator")
	}
}
