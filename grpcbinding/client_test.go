package grpcbinding

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
		return benzene.Created(greetResponse{Greeting: "Hello, " + req.Name + "!"})
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
	if len(result.Errors) != 1 || result.Errors[0].Message != "name is required" {
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
	if len(result.Errors) != 1 || result.Errors[0].Message != codes.Unavailable.String() {
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
		{name: "trailer wins verbatim", code: 0, trailer: metadata.Pairs(BenzeneStatusTrailer, "created"), want: benzene.StatusCreated},
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

// TestClient_Send_RecoversFieldsFromStatusDetails is the round trip this whole change exists for:
// a server-side failure carrying two field-scoped errors goes out over a real gRPC connection and
// arrives with both fields intact, rather than as one blob of joined prose (wire-contracts.md
// §4.2). It runs against a live server rather than calling the mapper directly, because the
// binding - not the mapping function - is where this was broken.
func TestClient_Send_RecoversFieldsFromStatusDetails(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newBuilderFor(t, validatedGreetHandler), greetRoutes()))
	client := NewClient(conn, clientRoutes())

	result := client.Send(withTimeout(t), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusValidationError {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusValidationError)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("Errors = %v, want both of the handler's errors", result.Errors)
	}
	want := []benzene.Error{
		{Message: "name is required", Field: "/name"},
		{Message: "greeting is too long", Field: "/greeting"},
	}
	for i, got := range result.Errors {
		if got.Message != want[i].Message {
			t.Errorf("Errors[%d].Message = %q, want %q", i, got.Message, want[i].Message)
		}
		if got.Field != want[i].Field {
			t.Errorf("Errors[%d].Field = %q, want %q", i, got.Field, want[i].Field)
		}
		// google.rpc.BadRequest has no agreed home for an error's `code` yet, so the ports
		// deliberately do not carry it - asserted so that inventing one is a test change, not a
		// silent divergence between this port and the others.
		if got.Code != "" {
			t.Errorf("Errors[%d].Code = %q, want empty until the spec says where a code travels", i, got.Code)
		}
	}
	if result.Payload != nil {
		t.Error("Payload should be nil for a failure")
	}
}

// TestClient_Send_FailureWithoutDetailsKeepsMessageOnlyError pins the fallback: a peer that sends
// no BadRequest details still yields exactly what this client always produced - one error carrying
// the status message.
func TestClient_Send_FailureWithoutDetailsKeepsMessageOnlyError(t *testing.T) {
	handler := func(_ context.Context, _ greetRequest) benzene.Result[greetResponse] {
		return benzene.NotFound[greetResponse]()
	}
	conn := newTestServer(t, UnaryServerInterceptor(newBuilderFor(t, handler), greetRoutes()))
	client := NewClient(conn, clientRoutes())

	result := client.Send(withTimeout(t), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`))

	if result.Status != benzene.StatusNotFound {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusNotFound)
	}
	if len(result.Errors) != 1 || result.Errors[0].Message != "not-found" || result.Errors[0].Field != "" {
		t.Errorf("Errors = %v, want a single message-only error", result.Errors)
	}
}

// TestClient_Send_ForeignPeerWithoutDetailsIsUnaffected is the same fallback for a peer that isn't
// Benzene at all - an ordinary gRPC server returning a plain status, with no details and no
// benzene-status trailer.
func TestClient_Send_ForeignPeerWithoutDetailsIsUnaffected(t *testing.T) {
	conn := &fakeConn{err: grpcstatuspkg.Error(codes.NotFound, "no such greeting")}
	client := NewClient(conn, clientRoutes())

	result := client.Send(context.Background(), benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`))

	if len(result.Errors) != 1 || result.Errors[0].Message != "no such greeting" {
		t.Errorf("Errors = %v, want the status message as a single error", result.Errors)
	}
}

func TestRecoverErrors(t *testing.T) {
	t.Run("field violations become structured errors", func(t *testing.T) {
		status, err := grpcstatuspkg.New(codes.InvalidArgument, "name is required").WithDetails(&errdetails.BadRequest{
			FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: "/name", Description: "name is required"}},
		})
		if err != nil {
			t.Fatalf("WithDetails() error = %v", err)
		}
		got := recoverErrors(status, "name is required")
		if len(got) != 1 || got[0].Message != "name is required" || got[0].Field != "/name" {
			t.Errorf("recoverErrors() = %v, want one error carrying the field", got)
		}
	})

	t.Run("a detail that isn't a BadRequest falls back to the message", func(t *testing.T) {
		status, err := grpcstatuspkg.New(codes.InvalidArgument, "boom").WithDetails(&errdetails.RetryInfo{})
		if err != nil {
			t.Fatalf("WithDetails() error = %v", err)
		}
		got := recoverErrors(status, "boom")
		if len(got) != 1 || got[0].Message != "boom" || got[0].Field != "" {
			t.Errorf("recoverErrors() = %v, want the message-only fallback", got)
		}
	})

	t.Run("no details at all falls back to the message", func(t *testing.T) {
		got := recoverErrors(grpcstatuspkg.New(codes.NotFound, "missing"), "missing")
		if len(got) != 1 || got[0].Message != "missing" {
			t.Errorf("recoverErrors() = %v, want the message-only fallback", got)
		}
	})
}
