# benzene-go — Project Guide for Claude Code

## What this is

`benzene-go` is the Go port of [Benzene](https://github.com/daniellepelley/Benzene), a
middleware-based library for hexagonal (ports-and-adapters) architecture. It lives in its own
repo (not a subdirectory of the main C# repo) for the same reasons a Go module normally gets
its own repo: an idiomatic `module` path, independent versioning/tagging, and a contributor
surface that doesn't require a .NET toolchain.

The main repo's `docs/specification/` is the source of truth for cross-language behavior -
`design-principles.md` (the "opinionated but optional" strategy and the `/benzene/`-prefixed
default service standard, which `httpbinding.EnvelopePath`/`HealthPath` and `meshd.ViewPath`
implement here), `core-concepts.md`, `wire-contracts.md`, `transport-bindings.md`,
`porting-guide.md`. When this
port and the spec disagree, the spec wins; fix the Go code, not the spec, unless the
disagreement reveals a genuine spec bug (rare - raise it explicitly if so).

## Documentation

Documentation written **in this repo is for the Go community**: idiomatic Go, the real module path
(`github.com/daniellepelley/benzene-go`), `net/http` and the `httpbinding` entry points, `go test`
— the concrete "how to build, host, test, and operate a Benzene service in Go". Write it the way a
Go developer expects to read it.

Do **not** restate the language-neutral material here. The concepts, wire contracts, status
vocabulary, mesh shapes, and the Cloud Service Profile are defined once, for every language, in the
cross-language [benzene](https://github.com/daniellepelley/Benzene/tree/main/docs/specification)
repo. **Link to the spec; don't duplicate it.** If you're writing something that is true for every
port rather than a Go idiom, it's a spec/guide change in the benzene repo — raise it there, not as
Go docs here. The website lets a reader pick their language and get the Go docs from this repo,
alongside the shared spec.

## Structure

- Root package (`benzene`) - Topic, Status, Result[T], Registry, Middleware/Pipeline, the
  DI-lite Container/Scope, the three-phase App lifecycle. No sub-package may import this in a
  cycle; everything else imports it.
- `wire/` - the transport-neutral message envelope. Deliberately has **no dependency on the
  rest of this module** - keep it that way (see the package doc comment).
- `httpstatus/` - the Benzene<->HTTP status mapping tables, cross-checked against
  `docs/specification/conformance/http-status-mapping.json` in the main repo.
- `grpcstatus/` - the Benzene<->gRPC status mapping tables, cross-checked against
  `docs/specification/conformance/grpc-status-mapping.json` in the main repo. Raw numeric
  gRPC status codes (not `google.golang.org/grpc/codes.Code`), so it stays zero-dependency
  like `httpstatus`; `grpcbinding` wraps the result as `codes.Code(...)`.
- `envelope/` - dispatches a `wire.Request` through a `Pipeline`, shared by `httpbinding`,
  `httpclient`, and `conformance`.
- `httpbinding/` - the HTTP transport binding (native + envelope-over-HTTP entry points).
- `httpclient/` - the HTTP outbound client.
- `healthcheck/` - reserved-topic health-check interception middleware, plus ready-made `Check`
  implementations for probing a dependency's reachability: `TCPCheck` (opens a TCP connection -
  `Benzene.HealthChecks.Tcp`) and `HTTPPingCheck` (GETs a URL, healthy only on 200, credentials
  stripped from the reported URL - `Benzene.HealthChecks.Http`). Both zero-dep (net/net-http) and
  report a coarse error *category*, never the raw message (which can leak infra detail to an
  unauthenticated health caller). `Benzene.HealthChecks.Disk` is deferred - Go has no portable
  free-space API (needs platform-specific syscalls behind build tags; see `ROADMAP.md`).
- `validation/` - request-validation building block: `Validated(validator, handler)` wraps a
  handler so an invalid request short-circuits to a `validation-error` result before the handler
  runs. The Go-idiomatic form of `Benzene.DataAnnotations`/`Benzene.FluentValidation`'s
  `ValidationMiddleware` - those are pipeline middleware because .NET's message-handler pipeline is
  typed; this port's pipeline is type-erased until the router dispatches, so validation composes at
  the **typed handler** as a plain wrapper at registration (no reflection, no struct-tag DSL, no
  dependency - the service writes an ordinary `Validate` function).
- `idempotency/` - de-duplicates redelivered messages on an at-least-once transport, matching
  `Benzene.Idempotency`. A **pipeline middleware** (unlike `validation`: it keys on a header, which
  is on the type-erased `InvocationContext`, so it needs no typed request): `Middleware(store, key)`
  atomically claims the key in a pluggable `Store` and runs the handler only the first time - a
  completed duplicate short-circuits to `ignored` (ack), an in-progress one to `conflict` (retry),
  and the winning attempt records `Complete` on success / `Release` on failure so a failure is never
  permanently suppressed. `InMemoryStore` (thread-safe, with **separate** short in-progress-lease and
  long completed-dedup TTLs so a crashed worker's key frees quickly instead of stalling every
  redelivery, + injectable clock) is the zero-dep default; a shared store (Redis) would be a separate
  module and must mirror that two-window design. Settlement runs on a cancellation-detached context;
  a store outage fails open.
- `ratelimiting/` - best-effort per-instance rate limiting, matching `Benzene.RateLimiting`. A
  pipeline `Middleware(limiter, cost)` that acquires each message's permit cost from a `Limiter`
  without queuing and short-circuits a rejected message to `too-many-requests`; the lease is held
  across the handler so a concurrency-style limiter releases correctly. The .NET package uses
  `System.Threading.RateLimiting` (a dependency); this keeps the root module dependency-free with a
  `Limiter` interface + a standard-library thread-safe `TokenBucket` default (an app plugs a
  different algorithm - e.g. a `golang.org/x/time/rate` adapter - behind the interface). Per-instance
  only: a fleet of N instances admits up to N× the rate - authoritative limiting belongs at the
  gateway.
- `resilience/` - retry middleware, matching `Benzene.Resilience` (retry-ONLY, zero-dep; circuit
  breaker/timeout/hedging/fallback are the Polly package's job in .NET, deferred here pending a
  dependency decision). `Middleware(opts...)` re-invokes the downstream pipeline with exponential
  backoff. Because the Go router funnels application failures onto `ic.Result` (not a Go error),
  retry has two triggers mirroring .NET's `shouldRetry`/`shouldRetryContext`: `WithRetryOnError`
  (default: any error except context cancellation) for a `next()` error, and `WithRetryOnResult`
  (default: never - the lever services actually set: `RetryUnsuccessful` or
  `RetryOnStatus(...)`) for an unsuccessful `ic.Result`. Backoff is `sleep = jitter(min(maxDelay,
  initialDelay*factor^attempt))` with the cap/jitter on the sleep only (the growth curve stays
  uncapped - AWS "full jitter", `FullJitter` helper provided), a context-cancellable sleep, and an
  injectable `WithSleep` for tests. Re-invokes the whole downstream pipeline, so place it above
  idempotent outbound/port calls, never on an inbound step that already wrote a response.
- `auth/` - authentication/authorization building block, matching `Benzene.Auth.Core`+`.Basic`
  (zero-dep). Go has no `ClaimsPrincipal`, so a `Principal` (name/roles/claims) is a plain value
  threaded on the context (`ContextWithPrincipal`/`PrincipalFromContext`). `BasicAuth(validate,
  realm)` is the RFC 7617 authentication middleware (reads `Authorization: Basic`, validates via a
  `BasicValidator` the app supplies - no default, no hardcoded-credential footgun - and either sets
  the principal + calls next, or short-circuits `unauthorized` with a `WWW-Authenticate` challenge;
  splits on the first `:` so a password may contain one). `Authorize(predicate)` /
  `RequireRole(role)` are the authorization middleware (`forbidden` when the principal is present
  but not permitted, `unauthorized` when absent). Header-based, so authentication is for
  HTTP-fronted pipelines.
- `cache/` - caching building block, matching the essence of `Benzene.Cache.Core` (zero-dep): a
  pluggable `Store` (Get/Set/Delete with per-entry TTL) + a generic read-through helper
  `GetOrLoad[T](ctx, store, key, ttl, load)` (the Go form of `CacheEntry.LazyLoad` - returns the
  cached value or calls `load` once and caches it). `InMemoryStore` (thread-safe, TTL + injectable
  clock) is the default; a shared store (Redis) implements the same interface in its own module.
  Degrades safely: a store read error is a miss, a write error is ignored, a `load` error is
  returned and not cached. Caching is a handler-level concern, so it's a helper, not a middleware.
- `saga/` - in-code saga orchestrator, matching `Benzene.Saga` (zero-dep, in-process): `New(stages)`
  runs `NewStage(steps)` in order; steps within a stage run concurrently; each `NewStep[T](forward,
  compensate)` pairs a forward action producing a `T` result with an optional compensation. On the
  first stage failure it compensates every completed effect in **reverse (LIFO) order** and returns a
  `Result` (`OutcomeSucceeded`/`RolledBack`/`PartiallyRolledBack`, `CompensationFailures` for orphaned
  effects). A `SagaContext` threads a stage's published results to later stages (`Set`/`Get[T]`, typed
  or keyed). `Run(ctx)` is the zero-overhead default; `RunWith(ctx, RunOptions{...})` adds an
  observability `StateStore` (`InMemoryStateStore`; records progress, does **not** resume a crashed
  saga) and a `RetryPolicy` (re-runs only a **clean** rollback, never a partial one, with exponential
  backoff). Go methods can't be generic, so the .NET fluent builder becomes free constructors and
  `Set`/`Get[T]` free functions; type-keyed context uses `reflect.TypeFor` at set/get time (off the
  message dispatch path, like `registry.go`). **In-process only, no durable crash-resume** - steps are
  closures that can't be rehydrated; for that, use Step Functions/Durable Functions/Temporal.
- `responseevents/` - the *response-as-event* pattern, matching `Benzene.ResponseEvents` (zero-dep):
  a pipeline `Middleware(publisher, mappings, opts...)` that, after the handler runs, republishes the
  handler's response payload as a follow-up event on a fire-and-forget transport (an SQS
  `order:create` handler's payload published as `order:created`). Each `Mapping` resolves
  `(sourceTopic, ic.Result) -> *Publication` and every matching mapping publishes (fan-out); `Map`
  (source->event, default: successful + payload, with `When`/`Project` options) and `CrudConvention`
  (`X:create`+`created` -> `X:created`) are the ready-made rules, plus custom `Mapping`
  implementations. `Publisher` is the outbound port; `NewSenderPublisher(client.Sender)` is the
  default (marshals + sends, an unsuccessful send is a publish failure). `PublishFailureMode`:
  `FailMessage` (default) replaces the result with `unexpected-error` and stops (nack/redeliver -
  handlers must be idempotent); `LogAndContinue` keeps the result and continues, with an optional
  `OnPublishError` hook (no forced logger dependency). The .NET package's AsyncAPI/spec-catalog and
  build-time unmapped-response diagnostic are **not** ported (Go has no spec generator here; mesh
  descriptor derivation is the introspection path); `Mapping.Covers` is kept for a future diagnostic.
  Reflect-free nil-payload check (dispatch path), so a *typed*-nil pointer payload publishes JSON
  `null` - a documented divergence from .NET's reference-null semantics.
- `cloudserviceprobe/` - the external, black-box conformance checker for the Cloud Service Profile
  (`docs/specification/cloud-service-profile.md` §2, §5), matching `Benzene.CloudService.Probe`
  (zero-dep: `net/http`+`encoding/json`+`crypto/rand`). `Run(ctx, client, baseURL, opts...)` hits a
  running service over HTTP and returns a **tri-state** `Report` (`Satisfied`/`NotSatisfied`/
  `Inconclusive`) for R1-R8 - never a bool, never a panic, never an error (unreachability and shape
  mismatches are verdicts). R8 (trace propagation) and half of R6 (register/heartbeat) are
  structurally unobservable from one service and stay `Inconclusive` by design; R7 goes
  `Inconclusive` the moment non-default paths are used. Deliberately **independent** of
  `httpbinding`/`healthcheck`/`mesh` - it keeps its own `/benzene/*` path constants and parses the
  wire shapes itself, because the profile is language-neutral and this tool must audit ANY Benzene
  Cloud Service over HTTP (a non-Go port included); do not couple it back to this port's models.
- `mesh/` - Phases 1-2 of `docs/design/mesh.md`: service `Descriptor` derived from the
  `Registry` (topics + JSON Schemas derived at startup from the `TReq`/`TRes` types the
  Registry captures at `Register` time, plus the contract `descriptorHash`),
  reserved-`benzene:mesh`-topic descriptor middleware, trace middleware + log exporter, and the
  issue feed's emitter half (`IssueMiddleware` + `PushIssueExporter`: source-side dedup by the
  normative §4.1 classification + SHA-256 fingerprint, delta counts, liveness flush).
  Schema derivation is the one sanctioned use of `reflect` - startup-only, never on the
  dispatch path. Every feed is independent and optional - degradation (nil registry, nil
  or failing exporter, unprovisioned descriptor endpoint) must reduce the mesh, never
  break the service. The `benzene:mesh:*` wire topics and shapes (wire.go) are shared with the
  collector and promoted to the main repo's spec (`docs/specification/mesh.md` there, now
  the normative text; `docs/design/mesh-spec-draft.md` is the historical draft), pinned by
  the vendored `mesh-*.json` fixtures in `conformance/`.
- `meshd/` - Phases 3-4 of `docs/design/mesh.md`: the collector - an ordinary Benzene
  service (register/heartbeat/traces/issues ingest + `benzene:mesh:query:*` read models over an
  in-memory store with a bounded trace ring; the `benzene:mesh:issues` feed of mesh.md §4.1
  merges failure signatures by fingerprint and surfaces them on the fleet view) and the Mesh
  View (an embedded,
  self-contained HTML page - no JS framework, per the zero-dependency stance). Consumer
  edges are derived from trace parentage at query time; providers from descriptors;
  nothing is declared. It must accept partial fleets: a missing feed renders a service
  as reduced (`missingFeeds`), never fails ingestion or queries.
- `awslambda/` - AWS Lambda binding: a hand-rolled Lambda Runtime API bootstrap loop, plus
  HTTP (API Gateway v2 / Function URL) and envelope adapters.
- `azurefunctions/` - Azure Functions custom-handler binding (the Data/Metadata JSON contract
  the Functions host forwards invocations over - Azure has no native Go worker): `Handler` for
  HTTP-triggered functions, `QueueHandler` for queue-shaped triggers (Storage Queue, Service
  Bus - failure is a non-2xx outer status, handing the message to the platform's own
  redelivery/poison-queue machinery), `CosmosHandler` for the Cosmos DB Change Feed trigger,
  `TimerHandler` for the Timer trigger (a scheduled tick carries no message, so it is fan-in like
  `CosmosHandler` - the topic is the scheduled job's identity named in code, the body is the tick's
  schedule info, and the outer 200/500 is for the host's monitoring since a timer has no redelivery),
  and `EventGridHandler` for the Event Grid trigger (matching `Benzene.Azure.Function.EventGrid`):
  one event per invocation (the host de-batches), the topic is the event **type** (Event Grid
  schema `eventType` or CloudEvents 1.0 `type`, told apart by `specversion`), the body is the
  event's `data`, and headers are the envelope's `id`/`subject`/`source`; a non-success dispatch is
  outer 500 so Event Grid's own retry + dead-letter machinery takes over (same fire-and-forget
  outer-200/500 as `QueueHandler`). Event Grid trigger only - the SDK-typed BlobStorage/EventHub
  triggers are deferred (isolated-worker shapes, see `ROADMAP.md`).
  The change-feed binding is **fan-in, not topic-routed** (core-concepts §3, streaming-shaped):
  the whole delivered batch of changed documents is one pipeline invocation - not one per
  document - dispatched to the topic named in code, whose handler takes the batch as a slice
  (`Handler[[]TDocument, TRes]`). Checkpointing is batch-level and on success only, so a failed
  dispatch is a non-2xx outer status that redelivers the entire batch (same convention as
  `QueueHandler`); the version-aware fan-in uses `envelope.DispatchTopicResult`. The self-hosted
  worker flavor (`Benzene.Azure.CosmosDb`) is deferred - it needs the Cosmos SDK (see
  `ROADMAP.md`).
- `awssqs/` - AWS SQS binding, in **its own Go module** (`awssqs/go.mod`) - one of the packages
  with a third-party dependency (`aws-sdk-go-v2/service/sqs`, needed for the outbound publish
  client; the inbound Lambda-trigger `Handler` is zero-dependency, like `awslambda`). See
  `RELEASING.md` for the multi-module layout and why.
- `cloudevents/` - CloudEvents 1.0 mapping, zero-dependency: wire envelope <-> CloudEvents
  (`type` <-> topic, `data` <-> body, other attributes <-> `ce-`-prefixed headers - the
  outbound direction only maps `ce-` headers back, documented lossiness), plus an inbound
  HTTP `Handler` for both content modes (binary and structured) with the queue bindings'
  ack/nack contract.
- `gcppubsub/` - Google Cloud Pub/Sub inbound binding, zero-dependency in the root module: an
  `http.Handler` for a push subscription's endpoint (base64 data + attributes in, ack/nack via
  the response status code), wire-contracts §2 topic resolution like `awssqs`/`awssns`. The
  outbound publish half needs the Pub/Sub SDK - a pending dependency decision (`ROADMAP.md`);
  if approved it gets its own module like `awssqs`/`awssns`.
- `awsdynamodb/` - DynamoDB Streams inbound binding, zero-dependency in the root module: a
  Lambda `Handler` for a stream event source mapping. Topic is `{tableName}:{eventName}` (table
  parsed from the stream ARN + INSERT/MODIFY/REMOVE), body is the record's image unmarshalled
  from DynamoDB AttributeValue format into plain JSON (NewImage, else OldImage, else Keys),
  headers are `dynamodb-`-prefixed metadata (no `_benzeneHeaders` - these come from table writes,
  not a Benzene publisher). No outbound half exists (writing to the table is the publish; the
  stream is read-only), so no SDK and no separate module. Records are ordered CDC, so processing
  is **sequential and stops at the first failure**, reporting that record's `SequenceNumber` for
  Lambda to checkpoint and redeliver - deliberately not `awssqs`'s concurrent fan-out. Matches
  `Benzene.Aws.Lambda.DynamoDb`.
- `awskinesis/` - Kinesis Data Streams inbound binding, zero-dependency in the root module and the
  direct sibling of `awsdynamodb`: a Lambda `Handler` for a stream event source mapping. Topic is
  the **stream name** parsed from the record's stream ARN (a Kinesis record has no per-record event
  type, so the stream itself is the routing key - unlike DynamoDB's `{tableName}:{eventName}`); body
  is the record's `data` base64-decoded into the raw bytes the producer wrote (typically JSON);
  headers are `kinesis-`-prefixed metadata (partition key, sequence number, ...). No outbound half
  (writing to the stream is the publish; the trigger is read-only), so no SDK and no separate module.
  Same ordered stop-at-first-failure + `SequenceNumber` checkpointing as `awsdynamodb` (AWS reads
  only the first reported failure for a Kinesis mapping). Matches `Benzene.Aws.Lambda.Kinesis`.
- `awskafka/` - AWS Lambda MSK/self-managed-Kafka inbound binding, zero-dependency in the root
  module and DISTINCT from the self-hosted `kafka` module (that one runs its own broker consumer
  loop needing `segmentio/kafka-go`; this is the zero-dep adapter for AWS's *managed* event source
  mapping, which delivers records as plain JSON). Topic is the **Kafka topic verbatim** (one Kafka
  topic = one Benzene topic - like the `kafka` module, unlike Kinesis's stream-name routing); body
  is the record's `value` base64-decoded into the producer's bytes; headers pass through verbatim
  (their byte-array wire form UTF-8 decoded), matching the self-hosted binding. Records are grouped
  by `{topic}-{partition}` and each partition is processed sequentially, **stopping at its first
  failure** and reporting `{partition, offset}` for that partition's resume - an **object-shaped**
  `batchItemFailures` identifier (unlike the string identifier of SQS/Kinesis/DynamoDB), so the
  mapping needs `FunctionResponseTypes: [ReportBatchItemFailures]`. Partitions are independent. No
  outbound half (producing to Kafka is the publish; the trigger is read-only), so no SDK and no
  separate module. Matches `Benzene.Aws.Lambda.Kafka`.
- `awss3/` - S3 event-notification inbound binding, zero-dependency in the root module: a Lambda
  `Handler` invoked by S3 when an object is created/removed. Topic is `{bucketName}:{eventName}`
  (bucket-qualified for consistency with `awsdynamodb`/`awskinesis`; the .NET binding routes on the
  bare event name - the S3 topic is a local routing concern, not a wire contract, so this diverges
  deliberately); body is the object **metadata** (bucket/key/size/etag - S3 doesn't deliver the
  object's contents); headers are `s3-`-prefixed. **Failure model differs from the stream siblings**:
  an S3-to-Lambda notification is an *async* invocation (no batch-item-failure mechanism), so a
  failed record returns a **Go error** - triggering AWS's async-invoke retry, the same posture as
  `awssns` - rather than a partial-batch report. This deliberately does NOT mirror the .NET binding's
  fire-and-forget swallow (which drops a failed event), per this port's no-silent-drop rule; S3 is
  at-least-once, so handlers must be idempotent. Matches `Benzene.Aws.Lambda.S3`.
- `awssns/` - AWS SNS binding, in **its own Go module** (`awssns/go.mod`) - same shape and same
  reason as `awssqs` (`aws-sdk-go-v2/service/sns` for the outbound publish client; the inbound
  `Handler`, subscribed directly to an SNS topic, is zero-dependency). Unlike SQS, a direct
  SNS-to-Lambda subscription has no batch/partial-failure concept, so `Handler` reports a failed
  notification by returning a Go error - triggering AWS's own async-invoke retry - rather than a
  `batchItemFailures` response body.
- `diagnostics/` - OpenTelemetry diagnostics middleware, in **its own Go module**
  (`diagnostics/go.mod`, needs `go.opentelemetry.io/otel` - API only, never the SDK; the
  application owns exporter/sampler setup, and without an SDK the no-op defaults apply). One
  server span per invocation + invocation metrics, same semantic identity (topic/version/
  Benzene status) as the mesh trace feed; the two compose over the same inbound traceparent.
- `awseventbridge/` - AWS EventBridge binding, in **its own Go module**
  (`awseventbridge/go.mod`, needs `aws-sdk-go-v2/service/eventbridge` for the outbound
  `PutEvents` client; the inbound rule-invoked `Handler` is zero-dependency), matching the
  main repo's spec exactly: topic is `detail-type` verbatim, body is the raw `detail` JSON,
  headers are `eventbridge-`-prefixed envelope metadata plus any wire headers embedded under
  the reserved `_benzeneHeaders` key inside `detail` (EventBridge has no native per-message
  attributes, so that's the only channel headers can travel on - embedded headers win on
  collision). `Client` embeds `_benzeneHeaders` only when the payload is a JSON object.
  Failure returns a Go error - async-invoke retry, like `awssns`.
- `kafka/` - Kafka binding, in **its own Go module** (`kafka/go.mod`, needs
  `segmentio/kafka-go` - a broker wire protocol isn't hand-rollable), matching
  `Benzene.Kafka.Core` exactly: one Kafka topic = one Benzene topic (verbatim, not a header
  or envelope), headers pass through verbatim both directions, body verbatim. `Consumer` loop
  (one scope per record, explicit commits; no broker-side redelivery/DLQ exists, so failures
  go to the `OnFailure` hook and are committed past) + outbound `Client` satisfying
  `client.Sender` (writes to the Kafka topic named after the Benzene topic, per message).
  Both halves depend on narrow interfaces (`MessageSource`, `MessageWriter`) so tests run
  against fakes, no live broker.
- `grpcbinding/` - gRPC binding, in **its own Go module** (`grpcbinding/go.mod`, needs
  `google.golang.org/grpc` + `google.golang.org/protobuf`) - **unary RPCs only**, a documented
  scope decision (see the package doc), not client/server/duplex streaming. Matches
  `Benzene.Grpc`(`.AspNet`)/`Benzene.Grpc.Client` exactly: `UnaryServerInterceptor` wraps an
  ordinary `*grpc.Server` and claims only the methods in its `Route` table (full method path,
  case-insensitive) - unclaimed methods fall through to the real generated service untouched
  ("the binding claims routes, it doesn't own the server"). Body is proto3-JSON bridged both
  directions via `protojson`; the `benzene-status` trailer is always set (several Benzene
  statuses collapse onto one gRPC code); `Client` satisfies `client.Sender` and recovers the
  precise status from that trailer, falling back to `grpcstatus.FromGRPC` otherwise. No
  reflection anywhere on the dispatch path - `Route.NewResponse`/`ClientRoute.NewRequest` are
  explicit factories (Go has no runtime type parameter to construct an arbitrary registered
  message from, unlike .NET generics).
- `conformance/` - the fixture runner; `testdata/*.json` are vendored copies from the main
  repo's `docs/specification/conformance/` (see `conformance/README.md` for how to re-sync).
- `examples/` - runnable example services: `helloworld` (plain HTTP),
  `mesh-helloworld` (collector + two meshed services, the Phases 1-4 demo), and one
  `<provider>-helloworld` per cloud deployment target (`aws-lambda-helloworld`,
  `aws-dynamodb-helloworld`, `aws-kinesis-helloworld`, `aws-kafka-helloworld`, `aws-s3-helloworld`,
  `azure-functions-helloworld`,
  `gcp-cloudrun-helloworld`, `aws-sqs-helloworld`, `aws-sns-helloworld`, `gcp-pubsub-helloworld`) -
  each with its own README stating the concrete deploy steps and exactly what was/wasn't verified
  without live cloud credentials. Plain Cloud Run needs no dedicated package (see
  `gcp-cloudrun-helloworld/README.md`); `gcppubsub` exists because the Pub/Sub push envelope is a
  concrete shape `httpbinding` alone can't cover - keep applying that bar to any new platform
  package. `aws-dynamodb-helloworld`, `aws-kinesis-helloworld`, `aws-kafka-helloworld`, and `aws-s3-helloworld` are consumer-only
  examples in the **root** module (like `aws-lambda-helloworld`), since the `awsdynamodb`/`awskinesis`/`awskafka`/`awss3` bindings are
  themselves zero-dependency (`aws-kafka-helloworld` targets AWS's *managed* MSK trigger, distinct from the self-hosted
  `kafka` module); `aws-sqs-helloworld` and `aws-sns-helloworld` are each their own module
  (depends on both the root module and its respective binding - would be a cycle inside either).
- `go.work` - ties the root module, `awssqs/`, `awssns/`, `awseventbridge/`, `kafka/`,
  `diagnostics/`, `grpcbinding/`, `examples/aws-sqs-helloworld/`, and
  `examples/aws-sns-helloworld/` together for local development (see `RELEASING.md`). Its
  `replace` lines are workspace-scoped only and never affect real external consumers.
- `.github/workflows/ci.yml` - build+test on every push/PR (gofmt, vet, build, race+cover test,
  plus a cross-compile smoke check per cloud example's real target). `.github/workflows/
  deploy-<provider>-helloworld.yml` (one per cloud example) - each gated on that provider's
  credential secret being set (`if: secrets.X != ''` at the job level) so it shows as skipped,
  not failed, until the repo owner configures deployment credentials. When adding a new cloud
  example, add its matching deploy workflow (with the same secret-gate pattern) in the same
  commit, and document the required secrets/variables in that example's own README.

## Before making changes

- Read the relevant section of the main repo's `docs/specification/` first (it's usually
  cloned/available alongside this repo when doing cross-repo work) - don't invent behavior that
  the spec already defines.
- Read an existing package's pattern (doc comments, error handling, test style) before adding a
  new one - follow it rather than introducing a new convention.
- Every package's tests are table-driven where the fixture shape allows it, using `t.Run` for
  subtests. Match this style.

## Conventions

- Language: Go, see `go.mod` for the minimum version.
- No third-party dependencies in the root module or any package without one already. The
  standard library covers everything there (generics for type-safe registration with
  type-erased storage, `context.Context` for cancellation/invocation-scoped values,
  `encoding/json` for the wire format) - zero dependencies is itself a selling point over the
  .NET original. `awssqs`, `awssns`, `awseventbridge`, `kafka`, `diagnostics`, and
  `grpcbinding` are the deliberate exceptions (needing `aws-sdk-go-v2` service clients for
  signed API calls, `segmentio/kafka-go` for the broker wire protocol,
  `go.opentelemetry.io/otel` for the OTel API, and `google.golang.org/grpc` +
  `google.golang.org/protobuf` for gRPC, which has no standard-library support at all) and
  each lives in its own module specifically so that exception doesn't spread. Ask before
  adding any other dependency; if one is approved, give it its own module rather than adding it
  to the root's `go.mod` - see `RELEASING.md`.
- Generics: used where they buy real type safety (`Handler[TReq, TRes]`, `Result[T]`,
  `GetService[T]`) but the `Registry` stores handlers behind a **type-erased** `erasedHandler`
  signature - Go generics can't hold heterogeneous `Result[T]` instantiations in one
  collection. Recover the concrete type via the `ResultInfo` interface, not reflection.
- DI: `Container`/`Scope` are a small first-party DI-lite object, not a reflection-based
  framework. A handler resolves a scoped/transient dependency via
  `benzene.ScopeFromContext(ctx)` + `benzene.GetService[T]`, since `Handler`'s signature
  carries no `*Scope` parameter (see `scope.go`'s `ContextWithScope` doc comment for why). A
  singleton dependency can just be captured in the handler's closure at registration time.
- Concurrency: `Container`/`Scope` use double-checked locking, not a lock held across a
  factory call - a factory is allowed to resolve other services from the same scope, and
  Go's `sync.Mutex` is not reentrant. Don't "simplify" this back to a single lock scope; it
  will deadlock the moment a factory does that (see the comment above `typedSingleton` in
  `scope.go`).

## Do NOT

- Do not add third-party dependencies without asking first.
- Do not change the `Handler[TReq, TRes]` signature (e.g. adding a `*Scope` parameter) without
  flagging it as a breaking change and considering how every existing package would need to
  change - it's meant to stay a plain, easily-testable function.
- Do not weaken `envelope`/`httpbinding`/`httpclient`/`awslambda`/`azurefunctions`'s "never
  return a Go error to the transport" rule - a missing handler, a conversion failure, a handler
  panic, and a transport-level failure are all supposed to become a `Result`/`wire.Response`
  (or the platform's own error-reporting shape - the Lambda Runtime API's error endpoint, the
  Functions host's outer-200/`Outputs.res.statusCode` split), never a panic that reaches the
  caller or a Go error the caller has to specially handle.
- Do not skip or weaken the conformance runner's fixtures to make it pass - if a fixture seems
  wrong, that's a signal to re-check the spec, not to loosen the assertion.
- Do not fabricate deployment config (a Dockerfile base image, an env var, a CLI flag) you
  can't verify - this repo has no live AWS/Azure/GCP credentials, so "verified" here means
  cross-compilation + unit tests against the platform's documented contract, not an actual
  deploy. Say so explicitly in the example's README (see `azure-functions-helloworld/README.md`
  for why it has no container Dockerfile) rather than presenting a guess as fact.

## Workflow expectations

- Run `gofmt -w .` before every commit; CI fails on unformatted files.
- Run `go vet ./... ./awssqs/... ./awssns/... ./awseventbridge/... ./kafka/...
  ./diagnostics/... ./grpcbinding/... ./examples/aws-sqs-helloworld/...
  ./examples/aws-sns-helloworld/... && go build (same paths) && go test (same paths) -race
  -cover` before considering a task
  complete - `./...` from the root does not cross a nested
  module boundary even with `go.work` present, so the nested modules need their own explicit
  path. Every non-test-only package should sit at 100% coverage, or just under it with the gap
  being a documented, genuinely-unreachable defensive branch (not an untested real code path) -
  if you can't tell which one a gap is, write the test that would prove it one way or the other
  rather than assuming.
- Keep commits scoped to one logical change (one package, one fix), matching this repo's
  history so far.
- New capability = new package + new tests + a README/doc-comment update in the same commit,
  not a follow-up "add tests later" commit.
