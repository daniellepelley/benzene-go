package benzenetest

import (
	"context"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

type greetRequest struct {
	Name string
}

type greetResponse struct {
	Greeting string
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func TestInvoke_SuccessReturnsTypedPayload(t *testing.T) {
	builder := newTestBuilder(t)

	result := Invoke[greetRequest, greetResponse](context.Background(), builder, benzene.NewTopic("greet"), nil, greetRequest{Name: "World"})

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload == nil || result.Payload.Greeting != "Hello, World!" {
		t.Errorf("Payload = %+v, want Greeting=Hello, World!", result.Payload)
	}
}

func TestInvoke_FailureReturnsErrorsAndNilPayload(t *testing.T) {
	builder := newTestBuilder(t)

	result := Invoke[greetRequest, greetResponse](context.Background(), builder, benzene.NewTopic("greet"), nil, greetRequest{Name: ""})

	if result.Status != benzene.StatusBadRequest {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusBadRequest)
	}
	if result.Payload != nil {
		t.Errorf("Payload = %+v, want nil", result.Payload)
	}
	if len(result.Errors) == 0 || result.Errors[0] != "name is required" {
		t.Errorf("Errors = %v, want [%q]", result.Errors, "name is required")
	}
}

func TestInvoke_MissingHandlerIsNotFound(t *testing.T) {
	builder := newTestBuilder(t)

	result := Invoke[greetRequest, greetResponse](context.Background(), builder, benzene.NewTopic("no:such:topic"), nil, greetRequest{Name: "World"})

	if result.Status != benzene.StatusNotFound {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusNotFound)
	}
}

func TestInvoke_PipelineErrorIsServiceUnavailable(t *testing.T) {
	builder := &benzene.ApplicationBuilder{
		Container: benzene.NewContainer(),
		Pipeline: benzene.NewPipeline(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
			return errors.New("boom")
		}),
	}

	result := Invoke[greetRequest, greetResponse](context.Background(), builder, benzene.NewTopic("greet"), nil, greetRequest{Name: "World"})

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) == 0 || result.Errors[0] != "boom" {
		t.Errorf("Errors = %v, want [%q]", result.Errors, "boom")
	}
}

func TestInvoke_EmptyPipelineIsUnexpectedError(t *testing.T) {
	builder := &benzene.ApplicationBuilder{
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(),
	}

	result := Invoke[greetRequest, greetResponse](context.Background(), builder, benzene.NewTopic("greet"), nil, greetRequest{Name: "World"})

	if result.Status != benzene.StatusUnexpectedError {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusUnexpectedError)
	}
}

func TestInvoke_HandlerCanResolveScopedServiceFromContext(t *testing.T) {
	container := benzene.NewContainer()
	benzene.AddScoped(container, "prefix", func(*benzene.Scope) string { return "Hi" })

	registry := benzene.NewRegistry()
	usesScope := benzene.Handler[greetRequest, greetResponse](func(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
		scope, ok := benzene.ScopeFromContext(ctx)
		if !ok {
			return benzene.UnexpectedError[greetResponse]("no scope on context")
		}
		prefix := benzene.GetService[string](scope, "prefix")
		return benzene.Ok(greetResponse{Greeting: prefix + " " + req.Name})
	})
	if err := benzene.Register(registry, benzene.NewTopic("greet"), usesScope); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: container,
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}

	result := Invoke[greetRequest, greetResponse](context.Background(), builder, benzene.NewTopic("greet"), nil, greetRequest{Name: "World"})

	if result.Payload == nil || result.Payload.Greeting != "Hi World" {
		t.Errorf("Payload = %+v, want Greeting=Hi World", result.Payload)
	}
}

func TestConvertResult_PayloadTypeMismatchLeavesNilPayload(t *testing.T) {
	result := benzene.Ok("a string payload")

	converted := convertResult[greetResponse](result)

	if converted.Status != benzene.StatusOk {
		t.Errorf("Status = %q, want %q", converted.Status, benzene.StatusOk)
	}
	if converted.Payload != nil {
		t.Errorf("Payload = %+v, want nil for a mismatched payload type", converted.Payload)
	}
}

func TestInvoke_HeadersAreVisibleToMiddleware(t *testing.T) {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	var seenHeaders map[string]string
	pipeline := benzene.NewPipeline(
		func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
			seenHeaders = ic.Headers
			return next(ctx)
		},
		benzene.RouterMiddleware(registry),
	)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: pipeline}

	Invoke[greetRequest, greetResponse](context.Background(), builder, benzene.NewTopic("greet"), map[string]string{"x-test": "1"}, greetRequest{Name: "World"})

	if seenHeaders["x-test"] != "1" {
		t.Errorf(`Headers["x-test"] = %q, want "1"`, seenHeaders["x-test"])
	}
}
