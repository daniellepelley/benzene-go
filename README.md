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
| `awslambda` | 90%+ | AWS Lambda binding: a hand-rolled Lambda Runtime API bootstrap loop (`Start`), plus `HTTPHandler` (API Gateway v2 / Function URL events) and `EnvelopeHandler` (direct invoke) |
| `azurefunctions` | 93%+ | Azure Functions custom-handler binding: `Handler` adapts the Data/Metadata JSON contract the Functions host forwards HTTP-triggered invocations over |
| `client` | 100% | Outbound-client decorators (`CorrelationDecorator`, `RetryDecorator`) over a transport-agnostic `Sender` interface |
| `cors` | 100% | Portable CORS middleware for HTTP-fronted services (origin/scheme/port matching, header wildcard, preflight) |
| `benzenetest` | 100% | In-process test host for *your* application's tests - `Invoke[TReq, TRes]` runs one pipeline invocation without real HTTP/Lambda/etc. |
| `awssqs` ([own module](RELEASING.md)) | 100% | AWS SQS binding: inbound `Handler` for a Lambda triggered by an SQS event source mapping (zero deps), outbound `Client` publishing via `SendMessage` (needs `aws-sdk-go-v2/service/sqs` - this repo's first third-party dependency) |
| `conformance` | n/a (test-only) | Runs this port against the fixtures vendored from the main repo's `docs/specification/conformance/` |
| `examples/helloworld` | - | A runnable example service - DI, health check, both HTTP entry points |
| `examples/aws-lambda-helloworld` | - | The same service, deployable to AWS Lambda (Dockerfile + SAM template) |
| `examples/azure-functions-helloworld` | - | The same service, deployable to Azure Functions (host.json/function.json) |
| `examples/gcp-cloudrun-helloworld` | - | The same service, deployable to Google Cloud Run (Dockerfile, no new package needed) |
| `examples/aws-sqs-helloworld` ([own module](RELEASING.md)) | - | A publisher Lambda (Function URL) forwarding to SQS + a consumer Lambda triggered by that queue |

Every non-test-only package sits at 100% coverage, or just under it where the gap is a
defensively-unreachable branch (documented at the call site - e.g. a `json.Marshal` failure on
a type that can't actually fail to marshal). Run `go test ./... -cover` to see current numbers.

## Deploying to a cloud provider

| Provider | Path | New package needed? |
|---|---|---|
| AWS | Lambda (container image) + a Function URL | `awslambda` - Lambda has no HTTP-server contract, only the Runtime API |
| AWS | Lambda triggered by SQS + publish-to-SQS | `awssqs` - its own module (needs the AWS SDK) |
| Azure | Azure Functions custom handler | `azurefunctions` - Azure has no native Go worker |
| Google Cloud | Cloud Run | None - Cloud Run's contract is "listen on `$PORT`", which `httpbinding` + `net/http` already satisfies |

Each `examples/*-helloworld` directory's README documents the concrete deploy steps and states
what was and wasn't verified in this repo's own CI sandbox. Each also has a matching GitHub
Actions workflow (`deploy-*.yml`, one per example) that runs that same deploy on every push to
`main` touching it - each is gated on its provider's credential secret being set, so the job
shows as **skipped** (not failed) until you add the secrets/variables listed in that example's
own README. None have been run for real from this repo (no live cloud credentials in this
sandbox) - only the code, cross-compilation, and unit tests have been verified here.

## Modules

This is a multi-module repo - see `RELEASING.md` for the full explanation (and for how Go's
decentralized module distribution works at all, if you're coming from an ecosystem with a
central package registry like NuGet). Short version: everything is one module except `awssqs`
and `examples/aws-sqs-helloworld`, which have their own `go.mod` because they need real
third-party dependencies the rest of the repo shouldn't carry. `go.work` ties them together for
local development.

## Scope

This port covers core-concepts.md and wire-contracts.md end to end (pipeline, DI, health
checks, HTTP binding + client, conformance, AWS/Azure/GCP deployment, an SQS binding) but does
**not** yet have: a gRPC binding, an SNS or Kafka binding (core-concepts.md's binding contract,
not this repo's scope decision, is what a binding must satisfy once one exists), or a
source-generator/codegen equivalent to the C# attribute-scanning sugar (per `porting-guide.md`,
explicit registration is the framework contract in every language; attribute scanning is
.NET-specific idiom, not something every port needs).

See `ROADMAP.md` for the fuller picture: what's next with zero new dependencies, what's next
*pending* a dependency decision, and what's deliberately not being ported at all (and why).

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
