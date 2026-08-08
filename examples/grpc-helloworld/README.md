# grpc-helloworld

The greet handler hosted over a gRPC unary RPC via `grpcbinding`, with an outbound
`grpcbinding.Client` making the round trip back - the Go counterpart of the .NET repo's
`examples/Grpc`, kept to the unary shape the binding covers.

## Run it

```
go run ./examples/grpc-helloworld
```

The server listens on `:50051` (override with `ADDR`), then makes one client round trip through
`grpcbinding.Client` and logs the greeting plus the recovered `benzene-status`.

## What this demonstrates

- **A gRPC server that hosts a Benzene handler**: `grpcbinding.UnaryServerInterceptor` claims the
  registered method (`/greet.Greeter/SayHello`) on an ordinary `*grpc.Server` and dispatches it
  through the pipeline; unclaimed methods would fall through to the native generated service
  untouched.
- **The outbound client**: `grpcbinding.Client` publishes a Benzene message as a unary call and
  recovers the precise Benzene status from the mandatory `benzene-status` trailer (several Benzene
  statuses collapse onto one gRPC code, so the trailer carries the exact one).
- **A port interface** (`Greeter`) resolved from the DI scope - swap the adapter without touching
  the handler, the hexagonal shape this project is named for.

## No `.proto` build step

Faithful to `grpcbinding`'s own tests, this example uses `google.protobuf.Struct` (`structpb`) as a
protoc-free stand-in message type: its well-known-type JSON mapping renders as a plain JSON object,
so it round-trips through the binding's proto3-JSON bridge exactly like a real generated message
would - no `.proto` file or `protoc` step needed to run the demo. A real service registers real
protoc-generated code and names it in the `Route`/`ClientRoute` `NewResponse` factories instead;
nothing else about the wiring changes.

## Module

This is its own Go module (`go.mod`) - it depends on the `grpcbinding` module (which needs
`google.golang.org/grpc` + `protobuf`), so it can't live in the zero-dependency root module. `go.work`
ties it together for local development. See `main_test.go` for the `benzenetest`-driven component
test (a real in-memory bufconn server wearing the same interceptor `main()` installs).
