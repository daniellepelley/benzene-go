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
| `mesh` | 100% | Phases 1-2 of [Benzene Mesh](docs/design/mesh.md): the service `Descriptor` derived from the live `Registry` - topics, per-topic request/response JSON Schemas derived at startup from the registered handler types, and the contract `descriptorHash` - plus reserved-`mesh`-topic descriptor middleware and `TraceMiddleware` + `LogExporter` emitting semantic per-invocation trace events. Every feed is optional - a service with only some feeds provisioned runs a reduced mesh, never a broken one |
| `meshd` | 100% | Phases 3-4 of [Benzene Mesh](docs/design/mesh.md): the collector - itself an ordinary Benzene service serving `mesh:register`/`mesh:heartbeat`/`mesh:traces` and the `mesh:query:*` read models over an in-memory store, plus the Mesh View (one embedded self-contained page, no JS framework). Accepts partial fleets: a service missing a feed renders as reduced, never breaks ingestion or queries |
| `awslambda` | 93%+ | AWS Lambda binding: a hand-rolled Lambda Runtime API bootstrap loop (`Start`), plus `HTTPHandler` (Function URL / API Gateway v2.0, API Gateway REST/v1.0, and ALB target-group events, detected per invocation) and `EnvelopeHandler` (direct invoke) |
| `azurefunctions` | 94%+ | Azure Functions custom-handler binding: `Handler` adapts the Data/Metadata JSON contract for HTTP-triggered functions; `QueueHandler` adapts queue-shaped triggers (Storage Queue, Service Bus), reporting a failed message via a non-2xx outer status so the platform's own redelivery/poison-queue machinery takes over |
| `client` | 100% | Outbound-client decorators (`CorrelationDecorator`, `RetryDecorator`) over a transport-agnostic `Sender` interface |
| `cors` | 100% | Portable CORS middleware for HTTP-fronted services (origin/scheme/port matching, header wildcard, preflight) |
| `benzenetest` | 100% | In-process test host for *your* application's tests - `Invoke[TReq, TRes]` runs one pipeline invocation without real HTTP/Lambda/etc. |
| `gcppubsub` | 100% | Google Cloud Pub/Sub inbound binding (zero deps): an `http.Handler` for a push subscription's endpoint - decodes the push envelope, resolves the topic per wire-contracts §2 (`topic` attribute or envelope-in-body), acks with 204 / nacks with 500 so Pub/Sub's own redelivery/dead-letter machinery handles failures. Outbound publishing needs the Pub/Sub SDK - a pending dependency decision (see `ROADMAP.md`) |
| `awssqs` ([own module](RELEASING.md)) | 100% | AWS SQS binding: inbound `Handler` for a Lambda triggered by an SQS event source mapping (zero deps), outbound `Client` publishing via `SendMessage` (needs `aws-sdk-go-v2/service/sqs` - this repo's first third-party dependency) |
| `awssns` ([own module](RELEASING.md)) | 100% | AWS SNS binding: inbound `Handler` for a Lambda subscribed directly to an SNS topic (zero deps; a failed notification returns a Go error, triggering AWS's own async-invoke retry, since SNS has no batch/partial-failure mechanism), outbound `Client` publishing via `Publish` (needs `aws-sdk-go-v2/service/sns`) |
| `conformance` | n/a (test-only) | Runs this port against the fixtures vendored from the main repo's `docs/specification/conformance/` |
| `examples/helloworld` | - | A runnable example service - DI, health check, both HTTP entry points |
| `examples/aws-lambda-helloworld` | - | The same service, deployable to AWS Lambda (Dockerfile + SAM template) |
| `examples/azure-functions-helloworld` | - | The same service, deployable to Azure Functions (host.json/function.json) |
| `examples/gcp-cloudrun-helloworld` | - | The same service, deployable to Google Cloud Run (Dockerfile, no new package needed) |
| `examples/gcp-pubsub-helloworld` | - | A Cloud Run service consuming a Pub/Sub push subscription via `gcppubsub.Handler` - publish with `gcloud pubsub topics publish`, no publisher code needed |
| `examples/aws-sqs-helloworld` ([own module](RELEASING.md)) | - | A publisher Lambda (Function URL) forwarding to SQS + a consumer Lambda triggered by that queue |
| `examples/aws-sns-helloworld` ([own module](RELEASING.md)) | - | A publisher Lambda (Function URL) forwarding to SNS + a consumer Lambda subscribed to that topic |
| `examples/mesh-helloworld` | - | The whole mesh story in one process: a `meshd` collector + two meshed services with a cross-service traced call - open the Mesh View and watch the derived fleet |

Every non-test-only package sits at 100% coverage, or just under it where the gap is a
defensively-unreachable branch (documented at the call site - e.g. a `json.Marshal` failure on
a type that can't actually fail to marshal). Run `go test ./... -cover` to see current numbers.

## Deploying to a cloud provider

| Provider | Path | New package needed? |
|---|---|---|
| AWS | Lambda (container image) + a Function URL | `awslambda` - Lambda has no HTTP-server contract, only the Runtime API |
| AWS | Lambda triggered by SQS + publish-to-SQS | `awssqs` - its own module (needs the AWS SDK) |
| AWS | Lambda subscribed to SNS + publish-to-SNS | `awssns` - its own module (needs the AWS SDK) |
| Azure | Azure Functions custom handler | `azurefunctions` - Azure has no native Go worker |
| Google Cloud | Cloud Run | None - Cloud Run's contract is "listen on `$PORT`", which `httpbinding` + `net/http` already satisfies |
| Google Cloud | Cloud Run consuming a Pub/Sub push subscription | `gcppubsub` (inbound only, zero deps) - the push envelope's base64/attributes/ack contract is the one GCP shape `httpbinding` can't cover |

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
central package registry like NuGet). Short version: everything is one module except `awssqs`,
`awssns`, `examples/aws-sqs-helloworld`, and `examples/aws-sns-helloworld`, which have their own
`go.mod` because they need real third-party dependencies the rest of the repo shouldn't carry.
`go.work` ties them together for local development.

## Scope

This port covers core-concepts.md and wire-contracts.md end to end (pipeline, DI, health
checks, HTTP binding + client, conformance, AWS/Azure/GCP deployment, SQS and SNS bindings) but
does **not** yet have: a gRPC binding, a Kafka binding (core-concepts.md's binding contract, not
this repo's scope decision, is what a binding must satisfy once one exists), or a
source-generator/codegen equivalent to the C# attribute-scanning sugar (per `porting-guide.md`,
explicit registration is the framework contract in every language; attribute scanning is
.NET-specific idiom, not something every port needs).

See `ROADMAP.md` for the fuller picture: what's next with zero new dependencies, what's next
*pending* a dependency decision, and what's deliberately not being ported at all (and why).
For how this project compares to the other ways of building cloud-portable services in Go
(Dapr, Go CDK, Watermill, Encore) and when you'd pick each, see
[docs/comparison.md](docs/comparison.md).

## Benzene Mesh

Benzene Mesh - a fleet-wide, multi-cloud view of every service, its topics/schemas, health,
and live traffic stats, derived from running services rather than declared in a catalog - is
designed in `docs/design/` ([mesh.md](docs/design/mesh.md), with a
[static mockup](docs/design/mesh-view-mockup.html) of the Fleet Overview screen and the
[research and positioning](docs/design/mesh-research.md) behind it). All phases of its
delivery plan are complete: the `mesh` and `meshd` packages and the `examples/mesh-helloworld`
demo above implement it, and the wire contracts are promoted and merged as the main repo's
`docs/specification/mesh.md` - now the normative text. The main repo's .NET implementation
(`Benzene.Mesh.Wire` + `Benzene.Mesh.Collector`) is the primary implementation of that
contract; this port is a fully conforming implementation - the contract was originally
extracted from it, the vendored `mesh-*.json` fixtures in `conformance/` pin it and pass, and
the two implementations have hosted each other's services in live cross-language fleets.

Both implementations also follow the spec's default service standard (the main repo's
`docs/specification/design-principles.md`): framework-provided HTTP surfaces mount under a
well-known `/benzene/` prefix - here `httpbinding.EnvelopePath` (`/benzene/invoke`),
`httpbinding.HealthPath` (`/benzene/health`), and `meshd.ViewPath` (`/benzene/fleet-ui`) - so
they read as infrastructure rather than domain endpoints, with every path overridable per
service. The same document records the wider "opinionated but optional" strategy the port
already embodies: message handlers, like everything else, are the steer, never a requirement.

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
