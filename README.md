# benzene-go

A Go port of [Benzene](https://github.com/daniellepelley/Benzene), a middleware-based library
for hexagonal (ports-and-adapters) architecture: a pipeline of middleware wraps calls to
"ports" (interfaces representing external boundaries - DB, HTTP, queues, etc), dispatched by
topic to a registered handler.

This repo is conformant with the main repo's language-neutral
[specification](https://github.com/daniellepelley/Benzene/tree/main/docs/specification) -
see `conformance/` for the fixtures this port runs against. The spec, not this README, is the
source of truth for cross-language behavior; when the two disagree, the spec wins and this
repo has a bug.

## Quickstart

```go
package main

import (
	"context"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func main() {
	registry := benzene.NewRegistry()
	benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler))

	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}

	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	http.ListenAndServe(":8080", httpbinding.Handler(builder, routes))
}
```

See `examples/helloworld/` for a complete version of this with dependency injection, a health
check, and both HTTP entry points wired through the three-phase `App` lifecycle.

## Packages

| Package | Coverage | What it is |
|---|---|---|
| `benzene` (root) | 100% | Topic, Status, Result[T], Registry, Middleware/Pipeline, RouterMiddleware, the DI-lite Container/Scope, the three-phase App lifecycle |
| `wire` | 100% | The transport-neutral message envelope (Request/Response/ErrorPayload) - no dependency on the rest of this module |
| `httpstatus` | 100% | The Benzene<->HTTP status mapping tables |
| `envelope` | 95%+ | Dispatches a `wire.Request` through a `Pipeline` and produces a `wire.Response` - shared by `httpbinding`, `httpclient`, and `conformance` |
| `httpbinding` | 95%+ | The HTTP transport binding: a native REST-style `Handler` (real HTTP status codes, explicit route table) and an `EnvelopeHandler` (the wire envelope over HTTP) |
| `httpclient` | 97%+ | The HTTP outbound client - one `Send(topic, headers, message)` method, mapping transport failures to `ServiceUnavailable` |
| `healthcheck` | 100% | Middleware that intercepts the reserved `healthcheck` topic and responds with the standard aggregate health response |
| `conformance` | n/a (test-only) | Runs this port against the fixtures vendored from the main repo's `docs/specification/conformance/` |
| `examples/helloworld` | - | A runnable example service |

Every non-test-only package sits at 100% coverage, or just under it where the gap is a
defensively-unreachable branch (documented at the call site - e.g. a `json.Marshal` failure on
a type that can't actually fail to marshal). Run `go test ./... -cover` to see current numbers.

## Scope

This port covers core-concepts.md and wire-contracts.md end to end (pipeline, DI, health
checks, HTTP binding + client, conformance) but does **not** yet have: a gRPC binding, a
message-queue binding (SQS/Kafka/etc - core-concepts.md's binding contract, not this repo's
scope decision, is what a binding must satisfy once one exists), or a source-generator/codegen
equivalent to the C# attribute-scanning sugar (per `porting-guide.md`, explicit registration is
the framework contract in every language; attribute scanning is .NET-specific idiom, not
something every port needs).

## Developing

```
go build ./...
go vet ./...
gofmt -l .              # should print nothing
go test ./... -race -cover
```

CI (`.github/workflows/ci.yml`) runs all of the above on every push/PR to `main`.

## License

MIT - see `LICENSE`.
