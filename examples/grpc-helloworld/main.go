// Command grpc-helloworld is the gRPC analogue of examples/helloworld: the same greet handler
// behind a port interface, hosted this time over a gRPC unary RPC via the grpcbinding package's
// server-side interceptor, with an outbound grpcbinding.Client making the round trip back. It is
// the Go counterpart of the .NET repo's examples/Grpc (a Greeter service + a client that calls
// it), kept to the unary shape grpcbinding covers.
//
// Faithful to grpcbinding's own tests, this example uses google.protobuf.Struct (structpb) as a
// protoc-free stand-in message type: its well-known-type JSON mapping renders as a plain JSON
// object, so it round-trips through grpcbinding's proto3-JSON bridge exactly like a real
// generated message would - no .proto file or protoc build step needed to run the demo. A real
// service registers real protoc-generated code and lists it in the Route/ClientRoute NewResponse
// factories instead; nothing else about the wiring changes.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/grpcbinding"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// Greeter is the "port": greetHandler depends on this interface, not on how a greeting is
// actually produced. Swapping politeGreeter for a localized or database-backed adapter would
// require no change to greetHandler or its registration - the hexagonal shape this whole project
// is named for.
type Greeter interface {
	Greet(name string) string
}

// politeGreeter is the adapter used by this example.
type politeGreeter struct{}

func (politeGreeter) Greet(name string) string { return "Hello, " + name + ", this is Benzene" }

// greeterKey is the typed DI key for the Greeter dependency - a struct key can't collide with
// another package's key, unlike a bare string (see helloworld's main.go for the rationale).
type greeterKey struct{}

// greetMethod is the full gRPC method path (generated code's own routing key). It is the topic
// key on the server Route and the target method on the client ClientRoute - the two ends agree on
// exactly this string.
const greetMethod = "/greet.Greeter/SayHello"

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

// greetHandler resolves its Greeter dependency via ScopeFromContext (see helloworld's main.go for
// why DI is scope-resolved rather than a Handler parameter) and answers with the greeting.
func greetHandler(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[greetResponse]("no DI scope on context")
	}
	greeter := benzene.GetService[Greeter](scope, greeterKey{})
	return benzene.Ok(greetResponse{Greeting: greeter.Greet(req.Name)})
}

// newApp is the composition root: the three-phase benzene.App both main() and the test boot from,
// so the test exercises exactly the wiring that ships.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.AddSingleton(container, greeterKey{}, func(_ *benzene.Scope) Greeter { return politeGreeter{} })
			if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

// serverRoutes is the server-side gRPC route table: the full method path claimed for Benzene
// dispatch, its topic, and the factory for a fresh response message (structpb.Struct here - see
// the package doc).
func serverRoutes() []grpcbinding.Route {
	return []grpcbinding.Route{
		{Method: greetMethod, Topic: benzene.NewTopic("greet"), NewResponse: func() proto.Message { return &structpb.Struct{} }},
	}
}

// clientRoutes is the outbound counterpart, keyed by topic: which method a Send to the "greet"
// topic invokes, and the request/response message factories bridging JSON <-> proto.
func clientRoutes() []grpcbinding.ClientRoute {
	return []grpcbinding.ClientRoute{
		{Topic: benzene.NewTopic("greet"), Method: greetMethod,
			NewRequest:  func() proto.Message { return &structpb.Struct{} },
			NewResponse: func() proto.Message { return &structpb.Struct{} }},
	}
}

// greetServiceDesc is the grpc.ServiceDesc a protoc-gen-go-grpc run would generate, hand-written
// here so the demo needs no .proto build step (see the package doc). Its single method decodes the
// request as a *structpb.Struct and defers to the interceptor chain; grpcbinding's interceptor
// claims the method and dispatches it through the Benzene pipeline, so the continuation (the
// "native" service implementation) is never reached for this claimed method.
var greetServiceDesc = grpc.ServiceDesc{
	ServiceName: "greet.Greeter",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "SayHello",
		Handler: func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			req := &structpb.Struct{}
			if err := dec(req); err != nil {
				return nil, err
			}
			info := &grpc.UnaryServerInfo{FullMethod: greetMethod}
			native := func(ctx context.Context, req any) (any, error) { return &structpb.Struct{}, nil }
			if interceptor == nil {
				return native(ctx, req)
			}
			return interceptor(ctx, req, info, native)
		},
	}},
}

// newServer builds a *grpc.Server with grpcbinding's interceptor installed and the (protoc-free)
// greet service registered - exactly the shape a real service uses, only with real generated code
// in place of greetServiceDesc.
func newServer(builder *benzene.ApplicationBuilder) *grpc.Server {
	server := grpc.NewServer(grpc.UnaryInterceptor(grpcbinding.UnaryServerInterceptor(builder, serverRoutes())))
	server.RegisterService(&greetServiceDesc, nil)
	return server
}

// greet makes the outbound round trip: it sends {"name": name} to the "greet" topic over conn via
// grpcbinding.Client and returns the greeting the server produced. The client recovers the precise
// Benzene status from the mandatory "benzene-status" trailer.
func greet(ctx context.Context, conn grpc.ClientConnInterface, name string) (greetResponse, benzene.Status, error) {
	client := grpcbinding.NewClient(conn, clientRoutes())
	body, err := json.Marshal(greetRequest{Name: name})
	if err != nil {
		return greetResponse{}, "", err
	}
	result := client.Send(ctx, benzene.NewTopic("greet"), nil, body)
	if result.Payload == nil {
		return greetResponse{}, result.Status, nil
	}
	var resp greetResponse
	if err := json.Unmarshal(*result.Payload, &resp); err != nil {
		return greetResponse{}, result.Status, err
	}
	return resp, result.Status, nil
}

func addrFromEnv() string {
	if addr := os.Getenv("ADDR"); addr != "" {
		return addr
	}
	return ":50051"
}

func main() {
	builder := newApp().Run()
	addr := addrFromEnv()

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	server := newServer(builder)

	go func() {
		log.Printf("grpc-helloworld serving %s on %s", greetMethod, addr)
		if err := server.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()
	defer server.GracefulStop()

	// Prove the whole thing end to end with one outbound round trip through grpcbinding.Client.
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	resp, status, err := greet(context.Background(), conn, "World")
	if err != nil {
		log.Fatalf("round trip: %v", err)
	}
	log.Printf("round trip [benzene-status: %s]: %s", status, resp.Greeting)
}
