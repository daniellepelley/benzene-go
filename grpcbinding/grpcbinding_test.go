package grpcbinding

import (
	"context"
	"net"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// This test file hand-writes the grpc.ServiceDesc a real protoc-gen-go-grpc run would
// generate, using google.protobuf.Struct (structpb) as a protoc-free stand-in message type -
// its JSON well-known-type mapping renders as a plain JSON object
// (structpb.NewStruct({"name":"World"}) <-> protojson `{"name":"World"}`), so it round-trips
// through this package's proto3-JSON bridge exactly like a real generated message would,
// with no .proto file or build step needed for the test itself. Real applications use real
// generated code; only this test's fixture is hand-rolled.

const (
	greetServiceName = "greet.GreetService"
	greetMethod      = "/" + greetServiceName + "/Greet"
	nativeMethod     = "/" + greetServiceName + "/Native"
	badRequestMethod = "/" + greetServiceName + "/BadRequestShapedMethod"
)

// nativeResponse is what the "native" (non-Benzene) method implementation returns - proves
// UnaryServerInterceptor's fallthrough reaches the real generated service unchanged.
func nativeResponse() *structpb.Struct {
	s, err := structpb.NewStruct(map[string]any{"native": true})
	if err != nil {
		panic(err)
	}
	return s
}

// methodHandler builds a protoc-gen-go-grpc-shaped MethodDesc.Handler for fullMethod: decode
// the request as a *structpb.Struct, run it through the interceptor chain (falling straight
// to the native continuation when no interceptor is installed), and always answer with
// nativeResponse() - the continuation is only ever reached for a method the interceptor
// doesn't claim, so which method this is only affects the FullMethod the interceptor sees.
func methodHandler(fullMethod string) func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		req := &structpb.Struct{}
		if err := dec(req); err != nil {
			return nil, err
		}
		continuation := func(ctx context.Context, req any) (any, error) {
			return nativeResponse(), nil
		}
		if interceptor == nil {
			return continuation(ctx, req)
		}
		return interceptor(ctx, req, &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}, continuation)
	}
}

// anyDecodingMethodHandler is methodHandler's counterpart for a method whose request type is
// *anypb.Any rather than *structpb.Struct - the vehicle
// TestUnaryServerInterceptor_RequestMarshalFailure uses to force a legitimate,
// non-contrived protojson.Marshal failure (an Any with an unresolvable TypeUrl decodes fine
// over the wire's binary protobuf encoding, but fails protojson).
func anyDecodingMethodHandler(fullMethod string) func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		req := &anypb.Any{}
		if err := dec(req); err != nil {
			return nil, err
		}
		continuation := func(ctx context.Context, req any) (any, error) {
			return nativeResponse(), nil
		}
		if interceptor == nil {
			return continuation(ctx, req)
		}
		return interceptor(ctx, req, &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}, continuation)
	}
}

var testServiceDesc = grpc.ServiceDesc{
	ServiceName: greetServiceName,
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Greet", Handler: methodHandler(greetMethod)},
		{MethodName: "Native", Handler: methodHandler(nativeMethod)},
		{MethodName: "BadRequestShapedMethod", Handler: anyDecodingMethodHandler(badRequestMethod)},
	},
}

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
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

func greetRoutes() []Route {
	return []Route{{Method: greetMethod, Topic: benzene.NewTopic("greet"), NewResponse: func() proto.Message { return &structpb.Struct{} }}}
}

// newTestServer starts a real *grpc.Server (registering testServiceDesc with the given
// interceptor) over an in-memory bufconn listener, and returns a connected *grpc.ClientConn
// plus a cleanup func. Using a live server (not calling the interceptor function directly)
// exercises the full wire round trip: proto marshal, bufconn transport, grpc-go's own codec
// decode, the interceptor, dispatch, and the response back.
func newTestServer(t *testing.T, interceptor grpc.UnaryServerInterceptor) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	server.RegisterService(&testServiceDesc, nil)
	go server.Serve(lis)
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func withTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
