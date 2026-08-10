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

This Quickstart wires the `ApplicationBuilder` directly, which is the shortest thing that runs.
A real service usually wraps that wiring in the three-phase `App[TConfig]` lifecycle
(`GetConfiguration` → `ConfigureServices` → `Configure`) instead — `App.Run()` produces the very
same `ApplicationBuilder`, just with a place for configuration and dependency registration to live.
See `examples/helloworld/` for the complete version with dependency injection, a health check, and
both HTTP entry points wired through that lifecycle.

## Scaffold a new service

The `templates/` directory holds starter projects - the `dotnet new` equivalent for the Go port,
driven by [`gonew`](https://pkg.go.dev/golang.org/x/tools/cmd/gonew)
(`golang.org/x/tools/cmd/gonew`). `gonew` instantiates a project by copying a template module and
rewriting its module path to the one you choose - no template engine, no placeholders to fill in
beyond the module path.

```bash
# API Gateway-fronted Lambda
go run golang.org/x/tools/cmd/gonew@latest \
  github.com/daniellepelley/benzene-go/templates/aws-apigateway example.com/myservice

# SQS-triggered Lambda
go run golang.org/x/tools/cmd/gonew@latest \
  github.com/daniellepelley/benzene-go/templates/aws-sqs example.com/myservice

cd myservice && go test ./...
```

Replace `example.com/myservice` with your own module path (its last segment becomes the new
directory name).

| Starter | Hosts |
|---|---|
| `aws-apigateway` | AWS Lambda fronted by API Gateway HTTP requests (plus direct wire-envelope invokes) |
| `aws-sqs` | AWS Lambda triggered by an SQS event source mapping |

Each starter generates a complete, buildable module: a composition root (`newApp` in `main.go`) +
a demo greet handler behind a `Greeter` port + the transport host + a `benzenetest` component test
that drives a real message through the whole pipeline + an AWS SAM `template.yaml` + a `Dockerfile`.

**Module resolution caveat:** the templates require `github.com/daniellepelley/benzene-go` (and, for
`aws-sqs`, the `awssqs` module) at their published version with **no `replace` directive** - a
shipped `replace` would break the moment `gonew` copies the module out of this repo. Until those
modules are tagged and published, a freshly generated project needs a `replace` **you** add pointing
at a local checkout:

```bash
# in the generated project, while benzene-go is not yet published:
go mod edit -replace github.com/daniellepelley/benzene-go=/path/to/benzene-go
go mod tidy
```

See [`templates/README.md`](templates/README.md) for the full model, per-template detail, and the
maintainer verification steps.

## Design notes for Go developers

Two things surprise Go developers on first read. Both are deliberate, and knowing *why* makes the
rest of the API predictable. (For the fuller idiom rationale and where the design bends toward Go
vs. stays consistent across language ports, see [docs/go-idioms-review.md](docs/go-idioms-review.md).)

### Handlers return `Result[T]`, not `(T, error)`

A handler is `func(context.Context, TReq) benzene.Result[TRes]` — there is no `error` return. That
is not an oversight: `Result[T]` carries a `Status` from a **fixed, wire-level status vocabulary**
(`ok`, `bad-request`, `not-found`, `unexpected-error`, …) that every Benzene language port and every
transport shares, so a handler's outcome maps identically onto an HTTP status, a gRPC code, or a
queue ack/nack. Returning a value instead of an `error` is also what lets a batch consumer turn one
bad message into a `bad-request` result rather than crashing the whole batch.

```go
func createOrder(_ context.Context, req orderRequest) benzene.Result[orderResponse] {
    if req.ID == "" {
        return benzene.BadRequest[orderResponse]("id is required") // not: return nil, errors.New(...)
    }
    return benzene.Ok(orderResponse{...})
}
```

Go `error` is still used everywhere it belongs — *infrastructure* speaks `error` (`Register`,
`Consumer.Run`, request decoding all return `error`); only the *handler boundary* speaks `Result`.
To map a Go `error` from a dependency at the handler edge, translate it to the status you want:
`if err != nil { return benzene.UnexpectedError[Res](err.Error()) }`.

### `Container`/`Scope` is DI-lite — prefer closures; use a typed key when you need the container

The `Container`/`Scope` is a small first-party DI helper (a named cross-language concept), **not** a
reflection framework. For most dependencies you don't need it at all: capture a singleton in the
handler's closure at registration time — plain Go, no lookup, no key.

```go
func newApp(orders OrderStore) benzene.App[Config] { /* orders is captured by the handler closure */ }
```

Reach for the container only for a *scoped* (per-invocation) or *transient* dependency, resolved via
`benzene.ScopeFromContext(ctx)` + `benzene.GetService[T]`. When you do, prefer a **typed key** over a
bare string, so keys can't collide and the compiler helps you:

```go
type orderStoreKey struct{}
benzene.AddScoped(container, orderStoreKey{}, func(*benzene.Scope) *OrderStore { return &OrderStore{} })
// in the handler:
scope, _ := benzene.ScopeFromContext(ctx)
store := benzene.GetService[*OrderStore](scope, orderStoreKey{})
```

`GetService` panics if a required service is missing (use `TryGetService` for the optional case) —
the "required dependency" contract, surfaced loudly at startup rather than as a nil later.

## Packages

| Package | Coverage | What it is |
|---|---|---|
| `benzene` (root) | 100% | Topic, Status, Result[T], Registry, Middleware/Pipeline, RouterMiddleware, the DI-lite Container/Scope, the three-phase App lifecycle |
| `wire` | 100% | The transport-neutral message envelope (Request/Response/ErrorPayload) - no dependency on the rest of this module |
| `httpstatus` | 100% | The Benzene<->HTTP status mapping tables |
| `grpcstatus` | 100% | The Benzene<->gRPC status mapping tables (wire-contracts §4.2) - raw numeric gRPC status codes, so this stays zero-dependency like `httpstatus`; a gRPC binding wraps the result as `codes.Code(...)` |
| `envelope` | 96%+ | Dispatches a `wire.Request` through a `Pipeline` and produces a `wire.Response` (merging any invocation-set response headers - see `benzene.SetResponseHeader`) - shared by `httpbinding`, `httpclient`, and `conformance` |
| `httpbinding` | 97%+ | The HTTP transport binding: a native REST-style `Handler` (real HTTP status codes, explicit route table with `{param}` path templating - captured segments arrive as `route-<name>` wire headers) and an `EnvelopeHandler` (the wire envelope over HTTP); handler-set response headers come back as real HTTP headers |
| `httpclient` | 97%+ | The HTTP outbound client - one `Send(topic, headers, message)` method, mapping transport failures to `ServiceUnavailable` |
| `healthcheck` | 100% | Middleware that intercepts the reserved `healthcheck` topic and responds with the standard aggregate health response, plus ready-made `Check`s: `TCPCheck` (opens a connection, `Benzene.HealthChecks.Tcp`), `HTTPPingCheck` (GET, healthy only on 200, URL credentials stripped, `Benzene.HealthChecks.Http`), and `DiskSpaceCheck` (host free-space self-check, `Benzene.HealthChecks.Disk`: `WithMinimumFreeBytes`/`WithWarningFreeBytes` gate health, else pure telemetry). All zero-dep and report a coarse error category, never the raw message; `DiskSpaceCheck`'s one platform call sits behind build tags (`syscall.Statfs` on unix, `GetDiskFreeSpaceExW` on windows, no `x/sys`) |
| `validation` | 100% | Request-validation building block (zero deps): `Validated(validator, handler)` wraps a handler so an invalid request short-circuits to a `validation-error` result before the handler runs (`Validator[T]`/`ValidatorFunc[T]` + a `Combine` composer). The Go-idiomatic form of `Benzene.DataAnnotations`/`Benzene.FluentValidation`'s ValidationMiddleware - a typed handler wrapper, since this port's pipeline is type-erased until dispatch |
| `idempotency` | 100% | De-duplicates redelivered messages on an at-least-once transport (zero deps), matching `Benzene.Idempotency`: a pipeline `Middleware(store, key)` that atomically claims a header-derived key in a pluggable `Store` and runs the handler only the first time - a completed duplicate is `ignored` (ack), an in-progress one is `conflict` (retry), the winning attempt records completion on success / releases on failure. `InMemoryStore` (separate short in-progress-lease and long completed-dedup TTLs, so a crashed worker's key frees fast; + clock) is the default; a store outage fails open |
| `ratelimiting` | 100% | Best-effort per-instance rate-limiting middleware (zero deps), matching `Benzene.RateLimiting`: `Middleware(limiter, cost)` acquires each message's permit cost from a `Limiter` and short-circuits a rejected message to `too-many-requests`. A `Limiter` interface + a standard-library thread-safe `TokenBucket` default (plug a different algorithm - e.g. a `golang.org/x/time/rate` adapter - behind the interface), so the root module stays dependency-free. Per instance, not a fleet-wide limit |
| `resilience` | 100% | Retry + timeout + bulkhead + fallback middleware (zero deps), matching most of `Benzene.Resilience`(`.Polly`) - only the circuit breaker (own module) and hedging (still to do) live elsewhere. `Middleware(opts...)` re-invokes the downstream with exponential backoff; two retry triggers since the router funnels failures onto `ic.Result` not a Go error - `WithRetryOnError` (default: any non-cancellation error) and `WithRetryOnResult` (default: never; pass `RetryUnsuccessful`/`RetryOnStatus(...)`). Backoff caps/jitters the sleep while growing the curve uncapped (AWS "full jitter", `FullJitter` helper); context-cancellable sleep. `Timeout(d)` bounds the downstream to a deadline (a cooperative `context.WithTimeout`, presented as a `StatusTimeout` result). `Bulkhead(maxConcurrency, opts...)` caps concurrent invocations (Polly's two-permit semaphore), shedding load fast to `too-many-requests` or, with `WithMaxQueue(n)`, letting callers wait (context-bounded). `Fallback(fn, opts...)` substitutes a degraded `ic.Result` when an attempt fails (same `*Unsuccessful`/`*OnStatus` triggers), e.g. degrading an open circuit breaker to a cached response. Place above idempotent outbound calls |
| `circuitbreaker` ([own module](RELEASING.md)) | 100% | Circuit-breaker middleware, the library-backed slice of `Benzene.Resilience.Polly` (needs `sony/gobreaker/v2`, hence its own module): `Middleware[T](cb, opts...)` runs the downstream inside a gobreaker `CircuitBreaker` - a `next()` error or a matching `ic.Result` (per `WithTripOnResult`, default `TripOnServerError`: only dependency-health statuses, so client errors never open the breaker; `TripUnsuccessful`/`TripOnStatus(...)` to broaden) counts as a failure; once open it **short-circuits without invoking the downstream** to a fail-fast status (`WithOpenStatus`, default `service-unavailable`). Open-state detected via a `called` flag (robust vs a downstream returning gobreaker's own sentinels); fail-fast result built at wiring time. Complements the zero-dep `resilience` (retry + timeout + bulkhead + fallback) |
| `auth` | 99.5% | Authentication/authorization building block (zero deps), matching `Benzene.Auth.Core`+`.Basic`+`.OAuth2`: a `Principal` (name/roles/claims) threaded on the context; `BasicAuth(validate, realm)` RFC 7617 middleware; `BearerAuth(validator, opts...)` OAuth2/JWT bearer middleware (the Go form of `OAuth2BearerMiddleware`) - validates a JWT and sets the principal, or short-circuits with a **generic** `unauthorized` (real reason only via `WithOnError`, never an oracle). JWT validation is pure stdlib (so zero-dep where .NET uses `Microsoft.IdentityModel`): explicit algorithm allowlist (RFC 8725 - `none`/off-list rejected up front), HS/RS/ES 256/384/512 with per-family typed keys (no cross-family confusion), iss/aud/exp/nbf/iat with clock skew; keys from `StaticKeys` or a caching `JWKSResolver` (+ OIDC discovery via `NewJWKSFromAuthority`). `Authorize`/`RequireRole`/`RequireScope` authorization middleware (`forbidden` when not permitted, `unauthorized` when absent) |
| `cache` | 100% | Caching building block (zero deps), matching the essence of `Benzene.Cache.Core`: a pluggable `Store` (Get/Set/Delete with per-entry TTL) + a generic read-through helper `GetOrLoad[T](ctx, store, key, ttl, load)` (the Go form of `CacheEntry.LazyLoad`). `InMemoryStore` (thread-safe, TTL + clock) is the default; a shared store (Redis) is its own module. Degrades safely - a store read error is a miss, a write error is ignored, a load error is returned and not cached |
| `saga` | 100% | In-code saga orchestrator (zero deps, in-process), matching `Benzene.Saga`: `New(stages...)` runs `NewStage(steps...)` in order, steps within a stage concurrently; each `NewStep[T](forward, compensate)` is a forward action + optional compensation. On the first stage failure it compensates every effect in reverse (LIFO) order and returns a `Result` (`Succeeded`/`RolledBack`/`PartiallyRolledBack`). A `SagaContext` threads results between stages (`Set`/`Get[T]`). `RunWith` adds an observability `StateStore` and a `RetryPolicy` (retries only a clean rollback). In-process only - no durable crash-resume (use Step Functions/Durable Functions/Temporal for that) |
| `responseevents` | 100% | Response-as-event middleware (zero deps), matching `Benzene.ResponseEvents`: `Middleware(publisher, mappings, opts...)` republishes a handler's response payload as a follow-up event on a fire-and-forget transport (an `order:create` handler's payload published as `order:created`). `Map` (source->event, `When`/`Project` options) and `CrudConvention` are the ready-made mappings (+ custom `Mapping`); every match publishes (fan-out). `NewSenderPublisher(client.Sender)` is the default outbound port. `FailMessage` (default) nacks/redelivers on a publish failure, `LogAndContinue` keeps the response (optional `OnPublishError` hook). The AsyncAPI spec-catalog half is not ported (Go has no spec generator; mesh descriptors are the introspection path) |
| `clienthealthcheck` | 100% | Consumer-side dependency health check (zero deps), matching `Benzene.Clients.HealthChecks`: a `ServiceCheck` probes a downstream Benzene provider's reserved `benzene:mesh` descriptor via an outbound `client.Sender` and reports the contract relationship - unreachable/no-descriptor → failed, reachable+matching hash → ok, reachable+drifted hash → warning (degraded, doesn't flip health). Reachability comes from the descriptor (served health-independently), never `benzene:healthcheck`, so it doesn't couple to the provider's transient health. `WithExpectedContractHash` compares the provider's live `descriptorHash` against the hash the consumer was built against. For a contracts diagnostic surface, not a liveness probe |
| `cloudserviceprobe` | 100% | External, black-box conformance checker for the Cloud Service Profile (zero deps, `net/http` only), matching `Benzene.CloudService.Probe`: `Run(ctx, client, baseURL, opts...)` hits a running service over HTTP and returns a tri-state `Report` (Satisfied/NotSatisfied/Inconclusive) for R1-R8 - never a bool, panic, or error. R8 and half of R6 are structurally unobservable from one service and stay Inconclusive by design. Independent of `httpbinding`/`mesh` (own path constants, own JSON parsing) so it can audit any Benzene Cloud Service, including a non-Go port |
| `cloudservice` | 100% | One-call Cloud Service Profile **builder** (zero deps), the assembly counterpart of `cloudserviceprobe` and the Go form of `Benzene.CloudService`: `New(name, registry, opts...)` wires the profile's synchronous HTTP surface - R1 hosted pipeline, R2 registry handlers, R3 health + `/benzene/health`, R4 envelope-invoke `/benzene/invoke`, R5 derived spec `/benzene/spec`, R7 default paths, plus the `benzene:mesh` descriptor - over one `ApplicationBuilder`, and returns the `http.Handler`, `Descriptor`, `Builder`, and a wiring `ProfileReport` - a full R1-R8 checklist. It's honest: `New` doesn't wire R6's outbound feeds (register/heartbeat/traces) or R8 (trace propagation), so `Satisfied()` is false for a `New`-only build and `Unsatisfied()` is the exact to-do list. `WithoutDescriptor()` drops R5/R6 per §4 exposure control |
| `logging` | 100% | Basic request logging/timing middleware, `log/slog` only (zero deps): one structured line per invocation - topic, status, duration - Info/Warn/Error by outcome. The dependency-free alternative to `diagnostics` |
| `mesh` | 100% | Phases 1-2 of [Benzene Mesh](docs/design/mesh.md): the service `Descriptor` derived from the live `Registry` - topics, per-topic request/response JSON Schemas derived at startup from the registered handler types, and the contract `descriptorHash` - plus reserved-`mesh`-topic descriptor middleware and `TraceMiddleware` + `LogExporter` emitting semantic per-invocation trace events. Every feed is optional - a service with only some feeds provisioned runs a reduced mesh, never a broken one |
| `meshd` | 100% | Phases 3-4 of [Benzene Mesh](docs/design/mesh.md): the collector - itself an ordinary Benzene service serving `benzene:mesh:register`/`benzene:mesh:heartbeat`/`benzene:mesh:traces` and the `benzene:mesh:query:*` read models over an in-memory store, plus the Mesh View (one embedded self-contained page, no JS framework). Accepts partial fleets: a service missing a feed renders as reduced, never breaks ingestion or queries |
| `openapi` | 98%+ | OpenAPI 3.0 document generation (zero deps), the Go form of `Benzene.Schema.OpenApi`: `Generate(desc, opts...)` turns a `mesh.Descriptor` (`mesh.Describe(registry, info)`) into an OpenAPI doc - each registered topic a POST operation whose request body is the topic's request schema and whose responses carry the response schema (200) and the Benzene failure vocabulary mapped to HTTP codes (`httpstatus`). Reuses mesh's derived schemas (no new reflection) and converts JSON Schema's nullable type-array to OpenAPI 3.0's `nullable: true`. `Handler(desc, opts...)` serves it over GET, the OpenAPI sibling of `mesh.SpecHandler`. (AsyncAPI for event topics is a documented follow-up - the descriptor doesn't classify sync vs event topics) |
| `awslambda` | 93%+ | AWS Lambda binding: a hand-rolled Lambda Runtime API bootstrap loop (`Start`), plus `HTTPHandler` (Function URL / API Gateway v2.0, API Gateway REST/v1.0, and ALB target-group events, detected per invocation) and `EnvelopeHandler` (direct invoke) |
| `azurefunctions` | 93%+ | Azure Functions custom-handler binding: `Handler` adapts the Data/Metadata JSON contract for HTTP-triggered functions; `QueueHandler` adapts queue-shaped triggers (Storage Queue, Service Bus), reporting a failed message via a non-2xx outer status so the platform's own redelivery/poison-queue machinery takes over; `CosmosHandler` adapts the Cosmos DB Change Feed trigger - fan-in, not topic-routed (the whole batch of changed documents is one invocation dispatched to a developer-named topic, handler takes the batch as a slice), checkpointing on success only (outer 500 redelivers the whole batch); `TimerHandler` adapts the Timer trigger (fan-in like Cosmos - a scheduled tick has no message, so the topic is named in code and the body is the tick's schedule info; outer 200/500, no redelivery) |
| `client` | 100% | Outbound-client decorators (`CorrelationDecorator`, `RetryDecorator`) over a transport-agnostic `Sender` interface. The spec's third client behavior, trace-context propagation, is `mesh.TraceContextDecorator` (in `mesh`, which owns the `Span` it forwards) - it composes over the same `Sender` |
| `inprocess` | 96%+ | An in-process `client.Sender`: dispatches straight to a handler pipeline built in the same runtime, without going over any wire. `PipelineSet` accumulates one named `*benzene.ApplicationBuilder` per module (each its own independent `Registry`/`Container`/`Pipeline`); `Sender` binds to one, `FanOutSender` binds to several and dispatches to all of them concurrently, isolating each target's failure. No shared/process-wide handler registry to collide over (unlike the .NET and TypeScript ports), so fan-out targets may share a literal topic |
| `cors` | 100% | Portable CORS middleware for HTTP-fronted services (origin/scheme/port matching, header wildcard, preflight) |
| `benzenetest` | 100% | In-process test host for *your* application's tests - `Invoke[TReq, TRes]` runs one pipeline invocation without real HTTP/Lambda/etc. |
| `cloudevents` | 99%+ | CloudEvents 1.0 mapping (zero deps): `type` ↔ topic, `data` ↔ body, other attributes ↔ `ce-`-prefixed wire headers; `Handler` accepts CloudEvents over HTTP in both content modes (binary `ce-*` headers and structured `application/cloudevents+json`) from Event Grid subscriptions, Knative triggers, EventBridge API destinations, etc.; `FromRequest`/`MarshalJSON` emit events for the outbound direction |
| `gcppubsub` | 100% | Google Cloud Pub/Sub inbound binding (zero deps): an `http.Handler` for a push subscription's endpoint - decodes the push envelope, resolves the topic per wire-contracts §2 (`topic` attribute or envelope-in-body), acks with 204 / nacks with 500 so Pub/Sub's own redelivery/dead-letter machinery handles failures. Outbound publishing needs the Pub/Sub SDK - a pending dependency decision (see `ROADMAP.md`) |
| `awssqs` ([own module](RELEASING.md)) | 100% | AWS SQS binding: inbound `Handler` for a Lambda triggered by an SQS event source mapping (zero deps), a self-hosted `Consumer` poller (`Run(ctx)` long-polls + deletes only successfully-dispatched messages, matching `Benzene.Aws.Sqs`), and an outbound `Client` publishing via `SendMessage` (needs `aws-sdk-go-v2/service/sqs`) |
| `diagnostics` ([own module](RELEASING.md)) | 100% | OpenTelemetry diagnostics middleware - one server span per invocation (named by topic, joined to the caller's W3C `traceparent`, `benzene.topic`/`benzene.status` attributes) plus `benzene.invocations`/`benzene.invocation.duration` metrics, and `TraceContextDecorator` (the OTel-path outbound client decorator that injects the active span context as a `traceparent`, sibling of `mesh.TraceContextDecorator`). Depends on the OTel *API* only; your app owns the SDK/exporter, and with no SDK installed the no-op defaults make it free |
| `kafka` ([own module](RELEASING.md)) | 100% | Kafka binding, matching the main repo's spec exactly (one Kafka topic = one Benzene topic, headers pass through verbatim, no envelope wrapping): a `Consumer` loop over a consumer group (one pipeline invocation + DI scope per record, explicit commits, an `OnFailure` hook since Kafka has no broker-side redelivery/DLQ to hand a failed message to) and an outbound `Client` satisfying `client.Sender` (needs `segmentio/kafka-go` - a broker wire protocol isn't hand-rollable) |
| `grpcbinding` ([own module](RELEASING.md)) | 100% | gRPC binding, unary RPCs only: `UnaryServerInterceptor` claims specific registered `Route`s (full method path → topic, case-insensitive) on an ordinary `*grpc.Server` - unclaimed methods fall through to the native generated service untouched, per spec - with proto3-JSON body bridging, incoming/outgoing metadata as wire headers, and the mandatory `benzene-status` trailer; an outbound `Client` satisfying `client.Sender` recovers the precise status from that trailer. Needs `google.golang.org/grpc` + `google.golang.org/protobuf` |
| `awseventbridge` ([own module](RELEASING.md)) | 96%+ | AWS EventBridge binding, matching the main repo's spec exactly: inbound `Handler` for a Lambda invoked by a rule (zero deps; topic is `detail-type` verbatim, body is the raw `detail` JSON, headers are `eventbridge-`-prefixed envelope metadata plus any `_benzeneHeaders` object embedded inside `detail`; a failed event returns a Go error, triggering AWS's async-invoke retry) and an outbound `Client` publishing via `PutEvents` (embeds headers under `_benzeneHeaders` when the message is a JSON object; needs `aws-sdk-go-v2/service/eventbridge`) |
| `awssns` ([own module](RELEASING.md)) | 100% | AWS SNS binding: inbound `Handler` for a Lambda subscribed directly to an SNS topic (zero deps; a failed notification returns a Go error, triggering AWS's own async-invoke retry, since SNS has no batch/partial-failure mechanism), outbound `Client` publishing via `Publish` (needs `aws-sdk-go-v2/service/sns`) |
| `awslambdaclient` ([own module](RELEASING.md)) | ~96% | Outbound Lambda-invoke `Client` (satisfies `client.Sender`), matching `Benzene.Clients.Aws.Lambda`: invokes a target Lambda with a wire envelope payload. `RequestResponse` parses the target's envelope response back into a `Result`; `Event` is fire-and-forget → `accepted`; a `FunctionError` → `unexpected-error`; transport failure → `service-unavailable` (needs `aws-sdk-go-v2/service/lambda`) |
| `awsstepfunctions` ([own module](RELEASING.md)) | ~96% | Outbound Step Functions `Client` (satisfies `client.Sender`), matching `Benzene.Clients.Aws.StepFunctions`: starts a state-machine execution with the wire envelope as `Input` → `accepted` (fire-and-forget). Optional idempotent `ExecutionName` (sanitized, 80-rune cap); `ExecutionAlreadyExists` on a same-name retry is an idempotent `accepted` (needs `aws-sdk-go-v2/service/sfn`) |
| `azureservicebus` ([own module](RELEASING.md)) | 100% | Azure Service Bus binding: outbound `Client` (topic as the reserved application property, headers as the others, body verbatim → `accepted`) + self-hosted `Worker` owning its own receive loop (`Run(ctx)`, the pull-loop counterpart of .NET's push `ServiceBusProcessor` and the sibling of `awssqs.Consumer`): completes only successfully-dispatched messages, settles a failure per `AckMode` (abandon→redeliver, default / dead-letter→quarantine); settlement on a cancellation-detached context (needs `azure-sdk-for-go/.../azservicebus`) |
| `azureeventhub` ([own module](RELEASING.md)) | 78%+ | Azure Event Hubs binding: outbound `Client` publishing one event as a batch-of-one → `accepted`, and a `Consumer` reading over a narrow `Receiver` with checkpointing handed back to a **caller-owned `Checkpoint` hook** (Event Hubs checkpointing needs a blob-store checkpoint store the app owns - a documented divergence). Coverage gap is the thin SDK-adapter constructors, uncoverable without live Event Hubs (needs `azure-sdk-for-go/.../azeventhubs/v2`) |
| `azureeventgrid` ([own module](RELEASING.md)) | 100% | Azure Event Grid binding: outbound CloudEvents `Client` (topic → CloudEvent `Type`, body → `Data` as `json.RawMessage` so a JSON payload rides as JSON not base64, headers → lowercased extension attributes → `accepted`) (needs `azure-sdk-for-go/.../eventgrid/azeventgrid`) |
| `azurequeuestorage` ([own module](RELEASING.md)) | 86%+ | Azure Queue Storage binding: outbound `Client` enqueuing the **whole `wire.Request` envelope** as the message text (verbatim, not base64) → `accepted`. Coverage gap is the defensive marshal-error branch (needs `azure-sdk-for-go/.../storage/azqueue`) |
| `azurecosmos` ([own module](RELEASING.md)) | 76%+ (core 100%) | Self-hosted Azure Cosmos DB Change Feed `Worker` (`Benzene.Azure.CosmosDb`), the standalone counterpart of the zero-dep `azurefunctions.CosmosHandler`: reads the change feed over a narrow `ChangeFeedReader` and dispatches each page **fan-in** (whole batch → one invocation to a code-named `Topic`, like `CosmosHandler`); stop-at-batch-failure (an unsuccessful dispatch/checkpoint doesn't advance the continuation token, so the batch redelivers); **caller-owned** `Checkpoint` hook (Cosmos needs an app-owned lease container) on a detached context; a `PollInterval` paces empty polls. Struct-fields + `Validate()`. Coverage gap is the live-only SDK adapter (needs `azure-sdk-for-go/.../data/azcosmos`) |
| `gcpfunctions` ([own module](RELEASING.md)) | 100% | Google Cloud **Functions Gen2** inbound binding (`GoogleCloud.Functions.Http` + `.PubSub`): `RegisterHTTP(name, builder, routes)` registers a Gen2 HTTP function serving `httpbinding.Handler` (thin), and `RegisterCloudEvent(name, builder, opts...)` registers a CloudEvent-triggered (Pub/Sub/Eventarc) function that maps the event onto a `wire.Request` by reusing `cloudevents.ToRequest` (identical to this port's other CloudEvents surface), dispatches, and returns nil on success / an error on failure so the platform retries - never a silent drop. `WithReservedNames`/`WithOnFailure`; framework signatures pinned by compile-time assertions (needs `functions-framework-go` + `cloudevents/sdk-go/v2`) |
| `gcppubsubclient` ([own module](RELEASING.md)) | 100% | Google Cloud Pub/Sub **outbound** client (the invoking counterpart of the inbound `gcppubsub` push handler): interface-driven `Publisher` + `NewTopicPublisher` adapter; `Send` publishes with topic + headers as Pub/Sub **attributes** (empty headers dropped), body as `Data` → `accepted`. **Requires go 1.25** (the one module forcing the workspace go directive + CI toolchain to 1.25; every other module stays 1.24.7) (needs `cloud.google.com/go/pubsub`) |
| `rabbitmq` ([own module](RELEASING.md)) | 100% | RabbitMQ binding: outbound `Client` publishing with the topic as **both** the routing key and a `"topic"` header, `Persistent` delivery, and a self-hosted `Consumer` (the AMQP sibling of `awssqs.Consumer`) that `Ack`s a successful delivery, `Nack`s a failure and requeues it exactly once (poison-message bounded to one retry) (needs `rabbitmq/amqp091-go`) |
| `awsdynamodb` | 100% | AWS DynamoDB Streams inbound binding (zero deps, root module): a Lambda `Handler` for a stream event source mapping. Topic is `{tableName}:{eventName}` (table parsed from the stream ARN + INSERT/MODIFY/REMOVE), body is the record's image unmarshalled from DynamoDB AttributeValue format into plain JSON (NewImage, else OldImage, else Keys). Records are ordered CDC, so processing is sequential and stops at the first failure, reporting that record's `SequenceNumber` for Lambda to checkpoint and redeliver - no outbound side (writing the table is the publish) |
| `awskinesis` | 100% | AWS Kinesis Data Streams inbound binding (zero deps, root module), the sibling of `awsdynamodb`: a Lambda `Handler` for a stream event source mapping. Topic is the stream name (parsed from the record's stream ARN - a Kinesis record has no per-record event type, so the stream is the routing key), body is the record's `data` base64-decoded into the producer's bytes (typically JSON), headers are `kinesis-`-prefixed metadata. Same ordered stop-at-first-failure + `SequenceNumber` checkpointing; no outbound side (writing the stream is the publish) |
| `awskafka` | 100% | AWS Lambda MSK/self-managed-Kafka inbound binding (zero deps, root module), DISTINCT from the self-hosted `kafka` module (that runs its own broker consumer loop; this is the zero-dep adapter for AWS's *managed* event source mapping, which delivers records as plain JSON). Topic is the Kafka topic verbatim (one Kafka topic = one Benzene topic, like the `kafka` module - unlike Kinesis's stream routing), body is the record's `value` base64-decoded, headers pass through verbatim. Records grouped by `{topic}-{partition}`; each partition processed sequentially, stopping at its first failure and reporting an object-shaped `{partition, offset}` (unlike the string identifier of SQS/Kinesis/DynamoDB), so the mapping needs `FunctionResponseTypes: [ReportBatchItemFailures]`; partitions are independent. No outbound side (producing to Kafka is the publish) |
| `awss3` | 100% | AWS S3 event-notification inbound binding (zero deps, root module): a Lambda `Handler` invoked by S3 on object create/remove. Topic is `{bucket}:{eventName}` (bucket-qualified, vs .NET's bare event name - a local routing concern), body is the object metadata (bucket/key/size/etag, not contents), headers are `s3-`-prefixed. An S3 notification is an async invocation, so a failed record returns a Go error (async-invoke retry, like `awssns`), never a silent drop; handlers must be idempotent |
| `conformance` | n/a (test-only) | Runs this port against the fixtures vendored from the main repo's `docs/specification/conformance/` |
| `examples/helloworld` | - | A runnable example service - DI, health check, both HTTP entry points |
| `examples/aws-lambda-helloworld` | - | The same service, deployable to AWS Lambda (Dockerfile + SAM template) |
| `examples/azure-functions-helloworld` | - | The same service, deployable to Azure Functions (host.json/function.json) |
| `examples/gcp-cloudrun-helloworld` | - | The same service, deployable to Google Cloud Run (Dockerfile, no new package needed) |
| `examples/gcp-pubsub-helloworld` | - | A Cloud Run service consuming a Pub/Sub push subscription via `gcppubsub.Handler` - publish with `gcloud pubsub topics publish`, no publisher code needed |
| `examples/aws-sqs-helloworld` ([own module](RELEASING.md)) | - | A publisher Lambda (Function URL) forwarding to SQS + a consumer Lambda triggered by that queue |
| `examples/aws-sns-helloworld` ([own module](RELEASING.md)) | - | A publisher Lambda (Function URL) forwarding to SNS + a consumer Lambda subscribed to that topic |
| `examples/aws-dynamodb-helloworld` | - | A consumer Lambda triggered by a DynamoDB table's stream via `awsdynamodb.Handler` - write to the table to drive it, no publisher code |
| `examples/aws-kinesis-helloworld` | - | A consumer Lambda triggered by a Kinesis data stream via `awskinesis.Handler` - `PutRecord` onto the stream to drive it, no publisher code |
| `examples/aws-kafka-helloworld` | - | A consumer Lambda triggered by an MSK `orders` topic via `awskafka.Handler` - produce a record to drive it, no publisher code (the MSK cluster is a prerequisite, passed by ARN) |
| `examples/aws-s3-helloworld` | - | A consumer Lambda invoked by an S3 bucket's ObjectCreated notifications via `awss3.Handler` - upload an object to drive it, no publisher code |
| `examples/mesh-helloworld` | - | The whole mesh story in one process: a `meshd` collector + two meshed services with a cross-service traced call - open the Mesh View and watch the derived fleet |
| `examples/http-helloworld` | - | The greet handler on a standalone `net/http` server via `httpbinding` - a net/http middleware wrapping the binding, plus graceful shutdown (the analog of the .NET `Asp` example) |
| `examples/grpc-helloworld` ([own module](RELEASING.md)) | - | The greet handler over a gRPC unary RPC via `grpcbinding` (protoc-free `structpb` stand-in messages) + an outbound `grpcbinding.Client` round trip |
| `examples/kafka-helloworld` ([own module](RELEASING.md)) | - | A Kafka consumer group running the greet handler via the `kafka` module + an outbound `kafka.Client` publish path |
| `examples/opentelemetry-helloworld` ([own module](RELEASING.md)) | - | The greet handler wrapped in `diagnostics` tracing middleware - one OTel span per invocation plus a nested adapter span, exported to stdout |

Every non-test-only package sits at 100% coverage, or just under it where the gap is a
defensively-unreachable branch (documented at the call site - e.g. a `json.Marshal` failure on
a type that can't actually fail to marshal). Run `go test ./... -cover` to see current numbers.

## Deploying to a cloud provider

| Provider | Path | New package needed? |
|---|---|---|
| AWS | Lambda (container image) + a Function URL | `awslambda` - Lambda has no HTTP-server contract, only the Runtime API |
| AWS | Lambda triggered by SQS + publish-to-SQS | `awssqs` - its own module (needs the AWS SDK) |
| AWS | Lambda subscribed to SNS + publish-to-SNS | `awssns` - its own module (needs the AWS SDK) |
| AWS | Lambda triggered by a DynamoDB table's stream | `awsdynamodb` (inbound only, zero deps) - the stream delivers change records as plain JSON, no SDK needed |
| AWS | Lambda triggered by a Kinesis data stream | `awskinesis` (inbound only, zero deps) - the stream delivers records as plain JSON (data base64-encoded), no SDK needed |
| AWS | Lambda triggered by an MSK / self-managed Kafka topic | `awskafka` (inbound only, zero deps) - the managed event source mapping delivers records as plain JSON (value base64-encoded), no SDK needed; distinct from the self-hosted `kafka` module |
| AWS | Lambda invoked by S3 event notifications | `awss3` (inbound only, zero deps) - S3 delivers the notification (object metadata) as plain JSON, no SDK needed |
| Azure | Azure Functions custom handler (HTTP, queue, Cosmos DB Change Feed, Timer, or Event Grid trigger) | `azurefunctions` - Azure has no native Go worker |
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
`awssns`, `awseventbridge`, `kafka`, `diagnostics`, `grpcbinding`, `examples/aws-sqs-helloworld`,
`examples/aws-sns-helloworld`, `examples/grpc-helloworld`, `examples/kafka-helloworld`, and
`examples/opentelemetry-helloworld`, which have their own `go.mod` because they need real
third-party dependencies the rest of the repo shouldn't carry. `go.work` ties them together for
local development.

## Scope

This port covers core-concepts.md and wire-contracts.md end to end (pipeline, DI, health
checks, HTTP binding + client, conformance, AWS/Azure/GCP deployment, SQS/SNS/Pub-Sub/Kafka/
gRPC bindings, CloudEvents) but does **not** yet have: gRPC's client-streaming/server-streaming/
duplex-streaming shapes (`grpcbinding` covers unary RPCs only - a documented scope decision,
not an oversight; see its package doc), or a source-generator/codegen equivalent to the C#
attribute-scanning sugar (per `porting-guide.md`, explicit registration is the framework
contract in every language; attribute scanning is .NET-specific idiom, not something every
port needs).

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
