package grpcbinding

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatuspkg "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeConn is a minimal grpc.ClientConnInterface for exercising Send's response-handling
// edge cases without needing a real network round trip (a live server can't easily be made
// to return an empty-message error or an unmarshalable response, since it always goes
// through this same package's own, well-behaved error/response construction).
type fakeConn struct {
	err      error
	populate func(reply any)
}

func (f *fakeConn) Invoke(_ context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	if f.populate != nil {
		f.populate(reply)
	}
	return f.err
}

func (f *fakeConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, grpcstatuspkg.Error(codes.Unimplemented, "fakeConn does not support streaming")
}

func clientRoutes() []ClientRoute {
	return []ClientRoute{{
		Topic:       benzene.NewTopic("greet"),
		Method:      greetMethod,
		NewRequest:  func() proto.Message { return &structpb.Struct{} },
		NewResponse: func() proto.Message { return &structpb.Struct{} },
	}}
}

func TestClient_Send_Success(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))
	client := NewClient(conn, clientRoutes())

	result := client.Send(withTimeout(t), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload == nil {
		t.Fatal("Payload is nil, want the response body")
	}
	var resp greetResponse
	if err := json.Unmarshal(*result.Payload, &resp); err != nil {
		t.Fatalf("json.Unmarshal(Payload) error = %v; payload = %s", err, *result.Payload)
	}
	if resp.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", resp.Greeting, "Hello, World!")
	}
}

func TestClient_Send_PreservesPreciseStatusViaTrailer(t *testing.T) {
	// The server always maps every success status to gRPC OK, so the *precise* Ok (as
	// opposed to Created/Accepted/etc, which would collapse to the same OK code) can only be
	// told apart by the benzene-status trailer - this proves Send reads it rather than just
	// inferring Ok from a bare OK code.
	registry := benzene.NewRegistry()
	handler := func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
		return benzene.CreatedResult(greetResponse{Greeting: "Hello, " + req.Name + "!"})
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](handler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
	conn := newTestServer(t, UnaryServerInterceptor(builder, greetRoutes()))
	client := NewClient(conn, clientRoutes())

	result := client.Send(withTimeout(t), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusCreated {
		t.Errorf("Status = %q, want %q (recovered from the trailer, not the coarse OK code)", result.Status, benzene.StatusCreated)
	}
}

func TestClient_Send_FailureRecoversStatusFromTrailer(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))
	client := NewClient(conn, clientRoutes())

	result := client.Send(withTimeout(t), benzene.NewTopic("greet"), nil, []byte(`{"name":""}`))

	if result.Status != benzene.StatusBadRequest {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusBadRequest)
	}
	if len(result.Errors) != 1 || result.Errors[0] != "name is required" {
		t.Errorf("Errors = %v, want [\"name is required\"]", result.Errors)
	}
	if result.Payload != nil {
		t.Error("Payload should be nil for a failure")
	}
}

func TestClient_Send_HeadersForwarded(t *testing.T) {
	registry := benzene.NewRegistry()
	var seenHeaders map[string]string
	capture := benzene.Middleware(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		seenHeaders = ic.Headers
		return next(ctx)
	})
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(capture, benzene.RouterMiddleware(registry)),
	}
	conn := newTestServer(t, UnaryServerInterceptor(builder, greetRoutes()))
	client := NewClient(conn, clientRoutes())

	client.Send(withTimeout(t), benzene.NewTopic("greet"), map[string]string{"x-correlation-id": "abc-123"}, []byte(`{"name":"World"}`))

	if seenHeaders["x-correlation-id"] != "abc-123" {
		t.Errorf(`Headers["x-correlation-id"] = %q, want %q`, seenHeaders["x-correlation-id"], "abc-123")
	}
}

func TestClient_Send_NoRouteRegisteredIsNotImplemented(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))
	client := NewClient(conn, nil)

	result := client.Send(withTimeout(t), benzene.NewTopic("no-such-topic"), nil, []byte(`{}`))

	if result.Status != benzene.StatusNotImplemented {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusNotImplemented)
	}
}

func TestClient_Send_MalformedRequestMessageIsServiceUnavailable(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))
	client := NewClient(conn, clientRoutes())

	result := client.Send(withTimeout(t), benzene.NewTopic("greet"), nil, []byte(`not valid json`))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

func TestClient_Send_UnclaimedNativeMethodStillDecodesResponse(t *testing.T) {
	// Exercises Send's success path against a method the interceptor never claims (routed
	// straight to the native handler) - proves the client-side proto3-JSON bridge works
	// independent of whether the far end is Benzene-shaped, as long as the wire shape matches.
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))
	routes := []ClientRoute{{
		Topic:       benzene.NewTopic("native"),
		Method:      nativeMethod,
		NewRequest:  func() proto.Message { return &structpb.Struct{} },
		NewResponse: func() proto.Message { return &structpb.Struct{} },
	}}
	client := NewClient(conn, routes)

	result := client.Send(withTimeout(t), benzene.NewTopic("native"), nil, []byte(`{}`))

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q (grpcstatus.FromGRPC fallback - no benzene-status trailer set)", result.Status, benzene.StatusOk)
	}
	var body map[string]any
	if err := json.Unmarshal(*result.Payload, &body); err != nil {
		t.Fatalf("json.Unmarshal(Payload) error = %v", err)
	}
	if native, _ := body["native"].(bool); !native {
		t.Errorf("Payload = %s, want the native handler's own response", *result.Payload)
	}
}

func TestClient_Send_EmptyErrorMessageFallsBackToCodeString(t *testing.T) {
	conn := &fakeConn{err: grpcstatuspkg.Error(codes.Unavailable, "")}
	route := ClientRoute{
		Topic:       benzene.NewTopic("greet"),
		Method:      greetMethod,
		NewRequest:  func() proto.Message { return &structpb.Struct{} },
		NewResponse: func() proto.Message { return &structpb.Struct{} },
	}
	client := NewClient(conn, []ClientRoute{route})

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if len(result.Errors) != 1 || result.Errors[0] != codes.Unavailable.String() {
		t.Errorf("Errors = %v, want [%q] (the code's own string when the status carries no message)", result.Errors, codes.Unavailable.String())
	}
}

func TestClient_Send_UnmarshalableResponseIsServiceUnavailable(t *testing.T) {
	// An Any with an unresolvable TypeUrl deterministically fails protojson.Marshal - a real
	// way to force the "read response" failure path without a contrived fake.
	conn := &fakeConn{populate: func(reply any) {
		if any, ok := reply.(*anypb.Any); ok {
			any.TypeUrl = "type.googleapis.com/does.not.Exist"
			any.Value = []byte{0x01}
		}
	}}
	route := ClientRoute{
		Topic:       benzene.NewTopic("greet"),
		Method:      greetMethod,
		NewRequest:  func() proto.Message { return &structpb.Struct{} },
		NewResponse: func() proto.Message { return &anypb.Any{} },
	}
	client := NewClient(conn, []ClientRoute{route})

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{}`))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

func TestRecoverStatus(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		trailer metadata.MD
		want    benzene.Status
	}{
		{name: "trailer wins verbatim", code: 0, trailer: metadata.Pairs(BenzeneStatusTrailer, "Created"), want: benzene.StatusCreated},
		{name: "no trailer falls back to code", code: 5, trailer: nil, want: benzene.StatusNotFound},
		{name: "empty trailer value falls back to code", code: 5, trailer: metadata.Pairs(BenzeneStatusTrailer, ""), want: benzene.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recoverStatus(tt.code, tt.trailer); got != tt.want {
				t.Errorf("recoverStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
