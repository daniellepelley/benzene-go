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

func TestDispatch_ApplicationDefinedStatusCarriesPayload(t *testing.T) {
	// A handler returning an application-defined (non-framework) status with a payload must
	// have that payload rendered as the body, not replaced by an error payload - the
	// extensibility promise, matching .NET's IsSuccessful (= !IsFailure).
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("partial"),
		benzene.Handler[greetRequest, greetResponse](func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
			return benzene.Result[greetResponse]{
				Status:  benzene.Status("partial-success"),
				Payload: &greetResponse{Greeting: "Partly " + req.Name},
			}
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	container := benzene.NewContainer()
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic: "partial", Headers: map[string]string{}, Body: `{"name":"World"}`,
	})

	if resp.StatusCode != "partial-success" {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, "partial-success")
	}
	var payload greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		t.Fatalf("body is not the payload (rendered as error?): %v; body = %s", err, resp.Body)
	}
	if payload.Greeting != "Partly World" {
		t.Errorf("Greeting = %q, want %q", payload.Greeting, "Partly World")
	}
}

func TestDispatch_ExplicitlySuccessfulFailureStatusCarriesPayload(t *testing.T) {
	// A result set explicitly successful (SetResult) with a failure status - the health-check
	// 503-but-render-the-body shape - carries its payload rather than an error payload, and the
	// envelope's statusCode is the failure status (which maps to HTTP 503 for a probe).
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("health"),
		benzene.Handler[greetRequest, greetResponse](func(_ context.Context, _ greetRequest) benzene.Result[greetResponse] {
			return benzene.SetResult(benzene.StatusServiceUnavailable, greetResponse{Greeting: "degraded"}, true)
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	container := benzene.NewContainer()
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	resp := Dispatch(context.Background(), pipeline, container, wire.Request{
		Topic: "health", Headers: map[string]string{}, Body: `{"name":"x"}`,
	})

	if resp.StatusCode != string(benzene.StatusServiceUnavailable) {
		t.Errorf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusServiceUnavailable)
	}
	var payload greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		t.Fatalf("body is not the payload (rendered as error?): %v; body = %s", err, resp.Body)
	}
	if payload.Greeting != "degraded" {
		t.Errorf("Greeting = %q, want %q", payload.Greeting, "degraded")
	}
}

func TestDispatchResult_SuccessFlagMatchesTheResultNotJustTheStatus(t *testing.T) {
	// The in-process success flag a queue binding acks/nacks on must follow the result's
	// IsSuccessful, which diverges from the wire status class for an application-defined failure
	// (Fail -> nack, not ack -> no silent message loss) and the health-check shape (SetResult
	// service-unavailable but successful -> ack, not a needless retry).
	cases := []struct {
		name       string
		handler    benzene.Handler[greetRequest, greetResponse]
		wantOK     bool
		wantStatus benzene.Status
	}{
		{"ok is successful", func(context.Context, greetRequest) benzene.Result[greetResponse] {
			return benzene.Ok(greetResponse{Greeting: "hi"})
		}, true, benzene.StatusOk},
		{"framework failure is not successful", func(context.Context, greetRequest) benzene.Result[greetResponse] {
			return benzene.ServiceUnavailable[greetResponse]("down")
		}, false, benzene.StatusServiceUnavailable},
		{"application-defined Fail is not successful", func(context.Context, greetRequest) benzene.Result[greetResponse] {
			return benzene.Fail[greetResponse](benzene.Status("partial-failure"), "boom")
		}, false, benzene.Status("partial-failure")},
		{"health-shape service-unavailable-but-successful is successful", func(context.Context, greetRequest) benzene.Result[greetResponse] {
			return benzene.SetResult(benzene.StatusServiceUnavailable, greetResponse{Greeting: "degraded"}, true)
		}, true, benzene.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := benzene.NewRegistry()
			if err := benzene.Register(registry, benzene.NewTopic("t"), tc.handler); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))
			resp, successful := DispatchResult(context.Background(), pipeline, benzene.NewContainer(), wire.Request{
				Topic: "t", Headers: map[string]string{}, Body: `{"name":"x"}`,
			})
			if successful != tc.wantOK {
				t.Errorf("successful = %v, want %v", successful, tc.wantOK)
			}
			if resp.StatusCode != string(tc.wantStatus) {
				t.Errorf("StatusCode = %q, want %q", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

// foreignResultInfo is a benzene.ResultInfo that does NOT implement the optional
// ResultIsSuccessful interface, exercising resultSuccessful's status-class fallback (every
// Result[T] implements it, so the fallback is otherwise unreachable through a real dispatch).
type foreignResultInfo struct{ status benzene.Status }

func (f foreignResultInfo) ResultStatus() benzene.Status { return f.status }
func (f foreignResultInfo) ResultErrors() []string       { return nil }
func (f foreignResultInfo) ResultPayload() any           { return nil }

func TestResultSuccessful_FallsBackToStatusForForeignResultInfo(t *testing.T) {
	if resultSuccessful(foreignResultInfo{status: benzene.StatusServiceUnavailable}) {
		t.Error("resultSuccessful(framework failure) = true, want false via the status fallback")
	}
	if !resultSuccessful(foreignResultInfo{status: benzene.StatusOk}) {
		t.Error("resultSuccessful(ok) = false, want true via the status fallback")
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
	// The Benzene status travels as benzeneStatus; Status is RFC 9457's integer HTTP code and is
	// absent on a non-HTTP transport like this one (wire-contracts.md §1.3).
	if errPayload.BenzeneStatus != string(benzene.StatusNotFound) {
		t.Errorf("errPayload.BenzeneStatus = %q, want %q", errPayload.BenzeneStatus, benzene.StatusNotFound)
	}
	if errPayload.Status != nil {
		t.Errorf("errPayload.Status = %v, want it omitted where there is no HTTP response", *errPayload.Status)
	}
	if errPayload.Type != wire.ProblemBase+"not-found" {
		t.Errorf("errPayload.Type = %q, want the §3.1 registry URI", errPayload.Type)
	}
	if errPayload.Detail == "" {
		t.Error("errPayload.Detail should describe the missing topic")
	}
	if len(errPayload.Errors) == 0 {
		t.Error("errPayload.Errors should list the message individually (§1.3: authoritative and ordered)")
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

func TestDispatch_InvocationSetResponseHeadersMergeOntoResponse(t *testing.T) {
	registry := benzene.NewRegistry()
	handler := func(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
		benzene.SetResponseHeader(ctx, "X-Request-Id", "abc-123")
		benzene.SetResponseHeader(ctx, "Content-Type", "application/vnd.example+json")
		return benzene.Ok(greetResponse{Greeting: "Hello " + req.Name})
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](handler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	resp := Dispatch(context.Background(), pipeline, benzene.NewContainer(), wire.Request{Topic: "greet", Body: `{"name":"World"}`})

	if got := resp.Headers["x-request-id"]; got != "abc-123" {
		t.Errorf(`Headers["x-request-id"] = %q, want %q`, got, "abc-123")
	}
	// An invocation-set header wins over the default content-type.
	if got := resp.Headers["content-type"]; got != "application/vnd.example+json" {
		t.Errorf(`Headers["content-type"] = %q, want the invocation-set override, got %q`, "application/vnd.example+json", got)
	}
}

func TestDispatch_ResponseHeadersMergeOntoErrorResponses(t *testing.T) {
	registry := benzene.NewRegistry()
	handler := func(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
		benzene.SetResponseHeader(ctx, "X-Request-Id", "abc-123")
		return benzene.BadRequest[greetResponse]("no")
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](handler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))

	resp := Dispatch(context.Background(), pipeline, benzene.NewContainer(), wire.Request{Topic: "greet", Body: `{"name":"World"}`})

	if resp.StatusCode != string(benzene.StatusBadRequest) {
		t.Fatalf("StatusCode = %q, want %q", resp.StatusCode, benzene.StatusBadRequest)
	}
	if got := resp.Headers["x-request-id"]; got != "abc-123" {
		t.Errorf(`Headers["x-request-id"] = %q, want %q (headers must survive a failure result)`, got, "abc-123")
	}
}

func TestWithResponseHeaders_AllocatesWhenResponseHasNoHeaderMap(t *testing.T) {
	// toResponse/errorResponse always populate Headers, so this guards the constructor
	// invariant rather than a reachable Dispatch path - tested directly so a future response
	// constructor without a header map can't turn the merge into a nil-map panic.
	ic := benzene.NewInvocationContext(benzene.NewTopic("t"), nil, nil, nil)
	ic.SetResponseHeader("x-request-id", "abc")

	resp := withResponseHeaders(wire.Response{StatusCode: string(benzene.StatusOk)}, ic)

	if got := resp.Headers["x-request-id"]; got != "abc" {
		t.Errorf(`Headers["x-request-id"] = %q, want %q`, got, "abc")
	}
}
