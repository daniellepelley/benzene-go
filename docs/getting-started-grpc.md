# gRPC Setup

The `grpcbinding` package lets an ordinary gRPC server's **unary** methods be served by Benzene
message handlers instead of hand-written service method bodies. It is a
`grpc.UnaryServerInterceptor` that *claims* specific registered methods for Benzene dispatch;
every method it doesn't claim falls through to the native generated service untouched — the
binding claims routes, it doesn't own the server.

Scope: **unary RPCs only.** Client-streaming, server-streaming, and duplex shapes are a
deliberate, documented gap (see the `grpcbinding` package doc), not covered here.

## Prerequisites

- Read [Getting started](getting-started.md) and skim the worked
  [`examples/helloworld`](../examples/helloworld) service first — this guide assumes you know how
  a handler, `Registry`, `Container`, and `Pipeline` fit together, and only shows where the gRPC
  binding slots in.
- Go 1.24+.
- Familiarity with gRPC in Go: `.proto` files, `protoc` + `protoc-gen-go` / `protoc-gen-go-grpc`,
  and the generated service/client code. This guide does not teach protobuf; it focuses on the
  binding.

`grpcbinding` is its own Go module (it needs `google.golang.org/grpc` and
`google.golang.org/protobuf`, which the zero-dependency root module doesn't carry):

```bash
go get github.com/daniellepelley/benzene-go/grpcbinding
```

## 1. Define your `.proto` and generate code

The binding sits behind ordinary generated gRPC code — you still write a real `.proto` and run
`protoc` exactly as you normally would:

```proto
syntax = "proto3";

package greet;
option go_package = "example/greetpb";

service GreetService {
  rpc Greet (HelloRequest) returns (HelloReply);
}

message HelloRequest {
  string name = 1;
}

message HelloReply {
  string greeting = 1;
}
```

Generate the Go types and service stubs with `protoc-gen-go` / `protoc-gen-go-grpc` as usual. That
gives you `greetpb.HelloRequest`, `greetpb.HelloReply`, the `GreetServiceServer` interface, and
`greetpb.RegisterGreetServiceServer`.

## 2. Write the handler

Business logic is an ordinary Benzene handler over plain Go structs — nothing gRPC-specific:

```go
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
```

**Field-name rule (important).** The binding bridges the request/response bodies through
proto3-JSON (`protojson`). The inbound generated message is marshalled to JSON and unmarshalled
into your request struct; your result struct is marshalled to JSON and unmarshalled into a fresh
generated response message. So your struct's `json:"..."` tags must match the protobuf message's
proto3-JSON field names (the lowerCamel proto field names — here `name` and `greeting`). Mismatched
names silently produce empty fields, exactly as a name-mismatched JSON bridge would.

## 3. Build the application

Register the handler and build the pipeline. This is the ordinary three-phase composition root
([core-concepts.md §7](https://benzene.app/docs/specification/core-concepts)); the returned
`*benzene.ApplicationBuilder` is what the binding reads:

```go
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
		},
	}
}
```

## 4. Wire the gRPC server

`grpcbinding.UnaryServerInterceptor(builder, routes)` returns a `grpc.UnaryServerInterceptor`.
Each `grpcbinding.Route` maps a **full gRPC method path** (matched case-insensitively against
`info.FullMethod`) to a Benzene `Topic`, plus a `NewResponse` factory that constructs a fresh
generated response message for the binding to fill in:

```go
func main() {
	builder := newApp().Run()

	routes := []grpcbinding.Route{
		{
			Method:      "/greet.GreetService/Greet",
			Topic:       benzene.NewTopic("greet"),
			NewResponse: func() proto.Message { return &greetpb.HelloReply{} },
		},
	}

	server := grpc.NewServer(grpc.UnaryInterceptor(
		grpcbinding.UnaryServerInterceptor(builder, routes)))

	// You still register a real generated service — gRPC's own routing/reflection needs one to
	// exist. Methods a Route claims never reach it; unclaimed methods fall through to it as normal.
	greetpb.RegisterGreetServiceServer(server, &greetServer{})

	lis, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Println("gRPC server listening on :8080")
	log.Fatal(server.Serve(lis))
}

// greetServer is the native service implementation. It embeds the generated Unimplemented base,
// so any method with no matching Route returns Unimplemented; a claimed method's body here is
// never invoked (the interceptor substitutes Benzene dispatch before it would run).
type greetServer struct {
	greetpb.UnimplementedGreetServiceServer
}
```

Chain it alongside other interceptors with `grpc.ChainUnaryInterceptor(...)` if you already have
some — it's an ordinary interceptor.

### What the binding does per claimed call

- **Body:** inbound generated message → JSON → dispatch; dispatch result JSON → `NewResponse()`
  instance → returned to the caller.
- **Headers:** incoming metadata becomes wire headers (binary `-bin` keys skipped); handler-set
  response headers (`benzene.SetResponseHeader`) become response metadata.
- **Status:** the Benzene status is mapped to a gRPC code via `grpcstatus`, and the raw status
  string is *always* set on the `benzene-status` trailer (`grpcbinding.BenzeneStatusTrailer`),
  success and failure alike — several Benzene statuses collapse onto one gRPC code, so the trailer
  is how a client recovers the precise one. A non-OK result becomes a `status.Error` carrying the
  joined error messages as its detail.
- **Cancellation:** a context already cancelled or past its deadline maps to `Canceled` /
  `DeadlineExceeded` directly.

## 5. Status mapping

The `grpcstatus` package (wire-contracts.md §4.2) drives the forward mapping:

| Benzene status | gRPC code |
|---|---|
| `ok`, `ignored`, `created`, `accepted`, `updated`, `deleted` | `OK` |
| `bad-request`, `validation-error` | `InvalidArgument` |
| `unauthorized` | `Unauthenticated` |
| `forbidden` | `PermissionDenied` |
| `not-found` | `NotFound` |
| `conflict` | `AlreadyExists` |
| `not-implemented` | `Unimplemented` |
| `service-unavailable` | `Unavailable` |
| `too-many-requests` | `ResourceExhausted` |
| `timeout` | `DeadlineExceeded` |
| `unexpected-error` / anything unrecognized | `Internal` |

## 6. Calling another gRPC service

`grpcbinding.NewClient(conn, routes)` returns a `*grpcbinding.Client` that satisfies
`client.Sender`, so it composes with the `client` decorators (`client.WithRetry`,
`client.WithCorrelationID`, …) like any other sender. Each `grpcbinding.ClientRoute` is keyed by topic and carries the method path plus
`NewRequest` / `NewResponse` factories:

```go
conn, err := grpc.NewClient("localhost:8080",
	grpc.WithTransportCredentials(insecure.NewCredentials()))
if err != nil {
	log.Fatalf("dial: %v", err)
}
defer conn.Close()

sender := grpcbinding.NewClient(conn, []grpcbinding.ClientRoute{
	{
		Topic:       benzene.NewTopic("greet"),
		Method:      "/greet.GreetService/Greet",
		NewRequest:  func() proto.Message { return &greetpb.HelloRequest{} },
		NewResponse: func() proto.Message { return &greetpb.HelloReply{} },
	},
})

result := sender.Send(ctx, benzene.NewTopic("greet"), nil, []byte(`{"name":"World"}`))
// result.Status is recovered from the benzene-status trailer verbatim when present, else from the
// coarse gRPC code via grpcstatus.FromGRPC. A missing route -> not-implemented; a marshalling or
// transport failure -> service-unavailable.
```

## 7. Testing

Test the handler through the pipeline with `benzenetest.Invoke` — transport-neutral, no gRPC server
or generated code needed, and it exercises the real middleware/DI/router path:

```go
func TestGreetHandler(t *testing.T) {
	builder := newApp().Run()

	result := benzenetest.Invoke[greetRequest, greetResponse](
		context.Background(),
		builder,
		benzene.NewTopic("greet"),
		nil,
		greetRequest{Name: "World"},
	)

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload == nil || result.Payload.Greeting != "Hello, World!" {
		t.Errorf("Payload = %+v, want greeting %q", result.Payload, "Hello, World!")
	}
}
```

For a full-stack round trip through a live server, see `grpcbinding`'s own `server_test.go` /
`client_test.go`, which drive a real `*grpc.Server` over an in-memory `bufconn` listener.

## Troubleshooting

- **My method still runs the native service body** — the `Route.Method` must be the full method
  path (`/<package>.<Service>/<Method>`), matching `info.FullMethod`. Matching is
  case-insensitive; a typo means the interceptor never claims it and it falls through.
- **Response fields are always empty** — your result struct's `json:"..."` tags don't match the
  protobuf message's proto3-JSON field names (lowerCamel proto field names, not `.proto` snake_case
  where they differ). See the field-name rule in step 2.
- **A client can't tell `created` from `ok`** — both map to gRPC `OK`; read the `benzene-status`
  trailer (`grpcbinding.BenzeneStatusTrailer`). `grpcbinding.Client` already prefers it.
- **`request does not implement proto.Message`** — the interceptor claimed a method whose request
  type isn't a generated protobuf message; only claim real generated RPC methods.

## See Also

- [Quickstart](../README.md#quickstart)
- [`examples/helloworld`](../examples/helloworld)
- [Wire contracts (status mapping)](https://benzene.app/docs/specification/wire-contracts)
- [Core concepts](https://benzene.app/docs/specification/core-concepts)
