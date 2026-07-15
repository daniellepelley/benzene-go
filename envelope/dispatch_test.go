package envelope

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	return benzene.Ok(greetResponse{Greeting: "Hello " + req.Name})
}

func newTestApp(t *testing.T) (*benzene.Registry, *benzene.Container, *benzene.Pipeline) {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	container := benzene.NewContainer()
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))
	return registry, container, pipeline
}

func TestDispatch_SuccessWithPayload(t *testing.T) {
	_, container, pipeline := newTestApp(t)

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic:   "greet",
		Headers: map[string]string{},
		Body:    `{"name":"World"}`,
	})

	if resp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusOk)
	}
	var payload greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		t.Fatalf("json.Unmarshal(resp.Body) error = %v; body = %s", err, resp.Body)
	}
	if payload.Greeting != "Hello World" {
		t.Errorf("Greeting = %q, want %q", payload.Greeting, "Hello World")
	}
	if resp.Headers["content-type"] != "application/json" {
		t.Errorf(`Headers["content-type"] = %q, want "application/json"`, resp.Headers["content-type"])
	}
}

func TestDispatch_MissingHandlerIsNotFound(t *testing.T) {
	_, container, pipeline := newTestApp(t)

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic: "no:such:topic", Headers: map[string]string{}, Body: "",
	})

	if resp.StatusCode != string(benzene.StatusNotFound) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusNotFound)
	}
	var errPayload wire.ErrorPayload
	if err := json.Unmarshal([]byte(resp.Body), &errPayload); err != nil {
		t.Fatalf("json.Unmarshal(resp.Body) error = %v; body = %s", err, resp.Body)
	}
	if errPayload.Status != string(benzene.StatusNotFound) {
		t.Errorf("errPayload.Status = %q, want %q", errPayload.Status, benzene.StatusNotFound)
	}
	if errPayload.Detail == "" {
		t.Error("errPayload.Detail should describe the missing topic")
	}
}

func TestDispatch_MalformedBodyIsBadRequest(t *testing.T) {
	_, container, pipeline := newTestApp(t)

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic: "greet", Headers: map[string]string{}, Body: "{not valid json",
	})

	if resp.StatusCode != string(benzene.StatusBadRequest) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusBadRequest)
	}
}

func TestDispatch_HandlerPanicIsServiceUnavailable(t *testing.T) {
	registry := benzene.NewRegistry()
	panicking := benzene.Handler[greetRequest, greetResponse](func(_ context.Context, _ greetRequest) benzene.Result[greetResponse] {
		panic("boom")
	})
	if err := benzene.Register(registry, benzene.NewTopic("panics"), panicking); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	container := benzene.NewContainer()
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic: "panics", Headers: map[string]string{}, Body: `{"name":"x"}`,
	})

	if resp.StatusCode != string(benzene.StatusServiceUnavailable) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusServiceUnavailable)
	}
}

func TestDispatch_NoResultProducedIsUnexpectedError(t *testing.T) {
	// An empty pipeline (no RouterMiddleware) never populates ic.Result - Dispatch must
	// still return a well-formed response rather than panicking on a nil ResultInfo.
	container := benzene.NewContainer()
	emptyPipeline := benzene.NewPipeline()

	resp := Dispatch(context.Background(), emptyPipeline, container, wire.Request{
		Topic: "greet", Headers: map[string]string{}, Body: "",
	})

	if resp.StatusCode != string(benzene.StatusUnexpectedError) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusUnexpectedError)
	}
}

func TestDispatch_PipelineErrorIsServiceUnavailable(t *testing.T) {
	container := benzene.NewContainer()
	failing := benzene.NewPipeline(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		return context.Canceled
	})

	resp := Dispatch(context.Background(), failing, container, wire.Request{Topic: "greet", Headers: map[string]string{}, Body: ""})

	if resp.StatusCode != string(benzene.StatusServiceUnavailable) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusServiceUnavailable)
	}
}

func TestDispatch_SuccessWithNoPayload(t *testing.T) {
	// core-concepts.md §5: payload is present on success "and optionally on failure" - a
	// success Result with no payload at all (Payload left nil) is valid, e.g. for a
	// fire-and-forget acknowledgement. Constructed directly rather than via Deleted(...),
	// which always wraps a concrete (even if zero-value) payload.
	registry := benzene.NewRegistry()
	noPayload := benzene.Handler[greetRequest, struct{}](func(_ context.Context, _ greetRequest) benzene.Result[struct{}] {
		return benzene.Result[struct{}]{Status: benzene.StatusDeleted}
	})
	if err := benzene.Register(registry, benzene.NewTopic("delete"), noPayload); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	container := benzene.NewContainer()
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic: "delete", Headers: map[string]string{}, Body: `{"name":"x"}`,
	})

	if resp.StatusCode != string(benzene.StatusDeleted) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusDeleted)
	}
	if resp.Body != "" {
		t.Errorf("Body = %q, want empty for a payload-less success result", resp.Body)
	}
}

func TestDispatch_UnmarshalablePayloadIsUnexpectedError(t *testing.T) {
	// toResponse's json.Marshal(payload) call can fail when a handler's declared TRes contains
	// a value encoding/json cannot represent (channels, funcs, complex numbers). This is a
	// handler-authoring bug, not a caller error, so it maps to UnexpectedError rather than
	// BadRequest.
	type unmarshalable struct {
		Ch chan int `json:"ch"`
	}
	registry := benzene.NewRegistry()
	broken := benzene.Handler[greetRequest, unmarshalable](func(_ context.Context, _ greetRequest) benzene.Result[unmarshalable] {
		return benzene.Ok(unmarshalable{Ch: make(chan int)})
	})
	if err := benzene.Register(registry, benzene.NewTopic("broken"), broken); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	container := benzene.NewContainer()
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic: "broken", Headers: map[string]string{}, Body: `{"name":"x"}`,
	})

	if resp.StatusCode != string(benzene.StatusUnexpectedError) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusUnexpectedError)
	}
}
