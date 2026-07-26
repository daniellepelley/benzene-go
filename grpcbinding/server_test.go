package grpcbinding

import (
	"context"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

func invokeGreet(t *testing.T, conn *grpc.ClientConn, name string, opts ...grpc.CallOption) (*structpb.Struct, error) {
	t.Helper()
	req, err := structpb.NewStruct(map[string]any{"name": name})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	resp := &structpb.Struct{}
	err = conn.Invoke(withTimeout(t), greetMethod, req, resp, opts...)
	return resp, err
}

func TestUnaryServerInterceptor_ClaimedMethodDispatches(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))

	resp, err := invokeGreet(t, conn, "World")
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got := resp.Fields["greeting"].GetStringValue(); got != "Hello, World!" {
		t.Errorf(`response["greeting"] = %q, want %q`, got, "Hello, World!")
	}
}

func TestUnaryServerInterceptor_UnclaimedMethodFallsThroughToNative(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))

	req, err := structpb.NewStruct(map[string]any{})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	resp := &structpb.Struct{}
	if err := conn.Invoke(withTimeout(t), nativeMethod, req, resp); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got := resp.Fields["native"].GetBoolValue(); !got {
		t.Errorf("response should be the native handler's own response, got %v", resp)
	}
}

func TestUnaryServerInterceptor_MethodMatchIsCaseInsensitive(t *testing.T) {
	routes := []Route{{Method: "/GREET.greetservice/GREET", Topic: benzene.NewTopic("greet"), NewResponse: func() proto.Message { return &structpb.Struct{} }}}
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), routes))

	resp, err := invokeGreet(t, conn, "World")
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got := resp.Fields["greeting"].GetStringValue(); got != "Hello, World!" {
		t.Errorf(`response["greeting"] = %q, want %q`, got, "Hello, World!")
	}
}

func TestUnaryServerInterceptor_FailureResultBecomesGRPCErrorWithTrailer(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))

	var trailer metadata.MD
	_, err := invokeGreet(t, conn, "", grpc.Trailer(&trailer))
	if err == nil {
		t.Fatal("Invoke() error = nil, want an RPC error for a failed dispatch")
	}
	st, ok := grpcstatus.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status error: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("Code() = %v, want InvalidArgument (BadRequest's forward mapping)", st.Code())
	}
	if st.Message() != "name is required" {
		t.Errorf("Message() = %q, want the handler's error detail", st.Message())
	}
	if got := trailer.Get(BenzeneStatusTrailer); len(got) != 1 || got[0] != "BadRequest" {
		t.Errorf("%s trailer = %v, want [BadRequest]", BenzeneStatusTrailer, got)
	}
}

func TestUnaryServerInterceptor_SuccessSetsTrailerToo(t *testing.T) {
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), greetRoutes()))

	var trailer metadata.MD
	if _, err := invokeGreet(t, conn, "World", grpc.Trailer(&trailer)); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got := trailer.Get(BenzeneStatusTrailer); len(got) != 1 || got[0] != "Ok" {
		t.Errorf("%s trailer = %v, want [Ok]", BenzeneStatusTrailer, got)
	}
}

func TestUnaryServerInterceptor_IncomingHeadersReachTheHandler(t *testing.T) {
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

	ctx := metadata.AppendToOutgoingContext(withTimeout(t), "x-correlation-id", "abc-123", "x-trace-bin", "should-be-skipped")
	req, err := structpb.NewStruct(map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	resp := &structpb.Struct{}
	if err := conn.Invoke(ctx, greetMethod, req, resp); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if seenHeaders["x-correlation-id"] != "abc-123" {
		t.Errorf(`Headers["x-correlation-id"] = %q, want %q`, seenHeaders["x-correlation-id"], "abc-123")
	}
	if _, ok := seenHeaders["x-trace-bin"]; ok {
		t.Error(`Headers["x-trace-bin"] should be skipped - binary ("-bin") keys have no flat-string form`)
	}
}

func TestUnaryServerInterceptor_HandlerResponseHeadersReachTheClient(t *testing.T) {
	registry := benzene.NewRegistry()
	handler := func(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
		benzene.SetResponseHeader(ctx, "x-request-id", "abc-123")
		return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
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

	var header metadata.MD
	if _, err := invokeGreet(t, conn, "World", grpc.Header(&header)); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got := header.Get("x-request-id"); len(got) != 1 || got[0] != "abc-123" {
		t.Errorf("response header x-request-id = %v, want [abc-123]", got)
	}
}

func TestUnaryServerInterceptor_NoResultAtAllTrailersUnknown(t *testing.T) {
	// A pipeline with no router at all - nothing downstream ever produces a Result.
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(), // no RouterMiddleware
	}
	conn := newTestServer(t, UnaryServerInterceptor(builder, greetRoutes()))

	var trailer metadata.MD
	_, err := invokeGreet(t, conn, "World", grpc.Trailer(&trailer))
	if err == nil {
		t.Fatal("Invoke() error = nil, want an error - dispatch produces UnexpectedError with no router")
	}
	if got := trailer.Get(BenzeneStatusTrailer); len(got) != 1 || got[0] != "UnexpectedError" {
		t.Errorf("%s trailer = %v, want [UnexpectedError]", BenzeneStatusTrailer, got)
	}
}

func TestUnaryServerInterceptor_RequestNotAProtoMessage(t *testing.T) {
	interceptor := UnaryServerInterceptor(newTestBuilder(t), greetRoutes())
	info := &grpc.UnaryServerInfo{FullMethod: greetMethod}

	_, err := interceptor(context.Background(), "not a proto.Message", info, func(ctx context.Context, req any) (any, error) {
		t.Fatal("continuation should not run for a claimed method")
		return nil, nil
	})
	if err == nil {
		t.Fatal("interceptor() error = nil, want Internal for a non-proto.Message request")
	}
	st, _ := grpcstatus.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("Code() = %v, want Internal", st.Code())
	}
}

func TestUnaryServerInterceptor_SetHeaderFailureWithNoServerStream(t *testing.T) {
	// Calling the interceptor directly (not through a live server) leaves ctx carrying no
	// *transport.Stream. envelope.Dispatch always sets a "content-type" response header on
	// any successful dispatch with a payload, so grpc.SetHeader always runs on this path -
	// and with no stream to write to, it returns an error, which a real call through a live
	// grpc.Server (whose context always carries one) never produces.
	interceptor := UnaryServerInterceptor(newTestBuilder(t), greetRoutes())
	info := &grpc.UnaryServerInfo{FullMethod: greetMethod}
	req, err := structpb.NewStruct(map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}

	_, err = interceptor(context.Background(), req, info, func(ctx context.Context, req any) (any, error) {
		t.Fatal("continuation should not run for a claimed method")
		return nil, nil
	})
	if err == nil {
		t.Fatal("interceptor() error = nil, want Internal - SetHeader has no stream to write to")
	}
	st, _ := grpcstatus.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("Code() = %v, want Internal", st.Code())
	}
}

func TestIncomingHeaders_NoMetadataOnContext(t *testing.T) {
	// A real call's context always carries at least an empty MD (grpc-go's own server
	// dispatch attaches one); ok=false only arises from a bare, non-gRPC context - unit
	// tested directly since context.Background() can never reach this deep into a live
	// interceptor call without failing earlier (see the SetHeader test above).
	got := incomingHeaders(context.Background())
	if len(got) != 0 {
		t.Errorf("incomingHeaders(context.Background()) = %v, want empty", got)
	}
}

func TestUnaryServerInterceptor_RequestMarshalFailure(t *testing.T) {
	// An Any with an unresolvable TypeUrl decodes fine over the wire (binary protobuf
	// doesn't care whether the type is registered) but deterministically fails
	// protojson.Marshal - a real way to force the "marshal request" failure path.
	routes := []Route{{Method: badRequestMethod, Topic: benzene.NewTopic("greet"), NewResponse: func() proto.Message { return &structpb.Struct{} }}}
	conn := newTestServer(t, UnaryServerInterceptor(newTestBuilder(t), routes))

	req := &anypb.Any{TypeUrl: "type.googleapis.com/does.not.Exist", Value: []byte{0x01}}
	resp := &structpb.Struct{}
	err := conn.Invoke(withTimeout(t), badRequestMethod, req, resp)
	if err == nil {
		t.Fatal("Invoke() error = nil, want Internal for an unmarshalable request")
	}
	st, _ := grpcstatus.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("Code() = %v, want Internal", st.Code())
	}
}

func TestUnaryServerInterceptor_ResponseUnmarshalFailure(t *testing.T) {
	// The registered response type isn't proto3-JSON-compatible with the dispatch result's
	// body - protojson.Unmarshal fails, which must become an Internal error, not a panic.
	registry := benzene.NewRegistry()
	handler := func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
		return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](handler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
	routes := []Route{{
		Method: greetMethod,
		Topic:  benzene.NewTopic("greet"),
		// structpb.ListValue expects a JSON array, but the dispatch body is a JSON object -
		// protojson.Unmarshal into it fails deterministically.
		NewResponse: func() proto.Message { return &structpb.ListValue{} },
	}}
	conn := newTestServer(t, UnaryServerInterceptor(builder, routes))

	_, err := invokeGreet(t, conn, "World")
	if err == nil {
		t.Fatal("Invoke() error = nil, want Internal for an unmarshalable response")
	}
	st, _ := grpcstatus.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("Code() = %v, want Internal", st.Code())
	}
}

func TestUnaryServerInterceptor_DeadlineExceededDuringDispatchBypassesStatusMapping(t *testing.T) {
	// A handler that outlives the call's deadline: by the time envelope.Dispatch returns,
	// ctx.Err() is already DeadlineExceeded, which UnaryServerInterceptor must map directly
	// rather than running the dispatch result through the ordinary Benzene status mapping.
	registry := benzene.NewRegistry()
	handler := func(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
		<-ctx.Done()
		return benzene.Ok(greetResponse{Greeting: "too late"})
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](handler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
	interceptor := UnaryServerInterceptor(builder, greetRoutes())
	info := &grpc.UnaryServerInfo{FullMethod: greetMethod}
	req, err := structpb.NewStruct(map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = interceptor(ctx, req, info, func(ctx context.Context, req any) (any, error) {
		t.Fatal("continuation should not run for a claimed method")
		return nil, nil
	})
	if err == nil {
		t.Fatal("interceptor() error = nil, want DeadlineExceeded")
	}
	st, _ := grpcstatus.FromError(err)
	if st.Code() != codes.DeadlineExceeded {
		t.Errorf("Code() = %v, want DeadlineExceeded", st.Code())
	}
}

func TestTrailerValue(t *testing.T) {
	// envelope.Dispatch never actually produces an empty StatusCode (every path sets one
	// from a Result), so this defensive fallback is unreachable through the interceptor
	// itself - unit tested directly.
	if got := trailerValue(""); got != "Unknown" {
		t.Errorf(`trailerValue("") = %q, want "Unknown"`, got)
	}
	if got := trailerValue("Ok"); got != "Ok" {
		t.Errorf(`trailerValue("Ok") = %q, want "Ok"`, got)
	}
}

func TestCancellationError(t *testing.T) {
	tests := []struct {
		name    string
		ctx     func() context.Context
		wantErr bool
		wantNil bool
	}{
		{name: "not cancelled", ctx: func() context.Context { return context.Background() }, wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cancellationError(tt.ctx())
			if tt.wantNil && err != nil {
				t.Errorf("cancellationError() = %v, want nil", err)
			}
		})
	}

	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		<-ctx.Done()
		err := cancellationError(ctx)
		if err == nil {
			t.Fatal("cancellationError() = nil, want DeadlineExceeded")
		}
		st, _ := grpcstatus.FromError(err)
		if st.Code() != codes.DeadlineExceeded {
			t.Errorf("Code() = %v, want DeadlineExceeded", st.Code())
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := cancellationError(ctx)
		if err == nil {
			t.Fatal("cancellationError() = nil, want Cancelled")
		}
		st, _ := grpcstatus.FromError(err)
		if st.Code() != codes.Canceled {
			t.Errorf("Code() = %v, want Canceled", st.Code())
		}
	})
}

func TestErrorDetail(t *testing.T) {
	tests := []struct {
		name string
		resp wire.Response
		want string
	}{
		{name: "detail from error payload", resp: wire.Response{StatusCode: "BadRequest", Body: `{"status":"BadRequest","detail":"name is required"}`}, want: "name is required"},
		{name: "no body falls back to status", resp: wire.Response{StatusCode: "NotFound", Body: ""}, want: "NotFound"},
		{name: "malformed body falls back to status", resp: wire.Response{StatusCode: "NotFound", Body: "not json"}, want: "NotFound"},
		{name: "nothing at all", resp: wire.Response{}, want: "Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorDetail(tt.resp); got != tt.want {
				t.Errorf("errorDetail() = %q, want %q", got, tt.want)
			}
		})
	}
}
