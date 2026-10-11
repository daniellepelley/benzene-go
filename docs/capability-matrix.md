# Capability Matrix — what benzene-go does, deliberately doesn't, and how to fill the gap

Benzene is honest about its boundaries. This page is the single place that states, for each
package or area of this port, **what it provides** (with the code that provides it), **what it
deliberately does not do (and why)**, and **how to solve the rest outside Benzene**. A gap that is
a design decision is stated with its reasoning; a gap that is simply unbuilt says "not
implemented" plainly — the two are never conflated here.

## The one idea behind every row

Benzene abstracts at the **business-logic boundary** — you write a message handler once and host
it anywhere — and **never at the transport or storage boundary**. If you wrap SQS behind a
generic queue interface, you lose the SQS-specific capabilities that were the reason to choose
SQS. So Benzene's answer to "does it abstract X?" is usually a deliberate **no**: every binding
exposes the native message/event, and rolling your own middleware is a first-class, supported
path, not an escape hatch.

Two corollaries specific to this port:

- **The root module is zero-dependency.** Every third-party dependency lives in its own Go module
  (`awssqs`, `kafka`, `grpcbinding`, …) so importing the core never pulls an SDK you didn't ask
  for. A "needs an SDK" boundary below is that policy, not a missing feature.
- **A database is not a transport.** Benzene delivers events *in*; persisting state is your
  handler's own code with its own SDK/driver.

## Core

| Capability | What benzene-go provides | What it deliberately does NOT do (and why) | How to solve the rest |
|---|---|---|---|
| **Pipeline & routing** | `Topic`, `Status`, `Result[T]`, `Registry`, `Middleware`/`Pipeline`, `RouterMiddleware`, three-phase `App` lifecycle (root module: `app.go`, `router.go`, `registry.go`, `middleware.go`) | Reflection/attribute-scanning handler discovery — this port is explicit-registration by design (`core-concepts.md` §8's "explicit registry" idiom for Go) | Register handlers explicitly on the `Registry`; the compiler is your discovery mechanism |
| **DI** | DI-lite `Container`/`Scope` + `ScopeFromContext` (`scope.go`) | Ship or integrate a reflection-based DI container — Go has no DI-container culture to bridge to | Constructor wiring in `main`; the `Container` covers per-invocation scoping |
| **Wire envelope & reserved names** | Transport-neutral envelope, `ResolveMetadataTopic`, `ReservedNames` — one injectable value for the configurable `topic`/correlation/version keys of `wire-contracts.md` §2 (`wire/envelope.go`, `wire/metadata.go`) | Impose the envelope on transports that already carry routing (Kafka topic, EventBridge `detail-type`) — the native key *is* the routing key there | Envelope-less transports use the Benzene envelope or a preset topic; otherwise route on the native key |
| **Versioning** | Inbound version selection from the `benzene-version`→`version`→`x-version` header fallback (`wire.ResolveVersion`) and from an HTTP `{version}` route segment; **exact-match only**, with a fallback to the unversioned handler (`router.go`, `router_version_test.go`) | Transparent payload up/down-casting (`versioning.md` §4 Mechanism B) — spec-optional, and the .NET implementation leans on reflection this zero-dependency port avoids. Exact-else-highest selection is **not implemented** — an upstream spec disagreement (`core-concepts.md` §2 vs `versioning.md` §3) is unsettled, so this port ships the conservative selector | Version your topics explicitly and register a handler per supported version; do payload up-casting in a small middleware if you need it |
| **Validation** | `Validated(validator, handler)` typed handler wrapper + `Validator[T]`/`Combine` (`validation/`) | A struct-tag/reflection validation DSL — the pipeline is type-erased until dispatch, so validation is a typed wrapper, not a pipeline middleware | Plug any validation library inside your `ValidatorFunc` |
| **In-process dispatch** | `inprocess` — an in-process `client.Sender` over named pipelines (`PipelineSet`), for the modular-monolith pattern | A container-wide outbound routing table (the .NET/TypeScript shape) — this port has no process-wide registry to hook into; a `Sender` is constructed and used directly like any other client | Construct the in-process `Sender` in `main` and pass it where a transport client would go |
| **Testing** | `benzenetest` in-process test host (`Invoke[TReq, TRes]`) | — | — |

## Transports — HTTP, gRPC, brokers

| Area | What benzene-go provides | What it deliberately does NOT do (and why) | How to solve the rest |
|---|---|---|---|
| **HTTP inbound** | Native REST-style routing + envelope-over-HTTP (`httpbinding/`, shared dispatch in `envelope/`); portable CORS middleware (`cors/`) | Own the HTTP server — the binding is an `http.Handler` you mount | Mount on any `net/http`-compatible server/mux |
| **HTTP outbound** | `httpclient.Client` (one `Send`), satisfying `client.Sender`; decorators `WithCorrelationID`/`WithRetry` (`client/`) | Manage connection pooling/auth for you | Supply your own configured `*http.Client` |
| **gRPC** | `grpcbinding` (own module): `UnaryServerInterceptor` claiming only routed methods, proto3-JSON bridging, `benzene-status` trailer, outbound `Client`; status tables in `grpcstatus/` | Streaming RPCs (client/server/duplex) — **not implemented**, a documented gap; and no codegen of service stubs — the binding claims routes, it doesn't own the server | Register real protoc-generated services; unmatched methods fall through untouched |
| **Kafka (self-hosted)** | `kafka` (own module, `segmentio/kafka-go`): consumer-group `Consumer` (one invocation + scope per record, explicit commits) + outbound `Client`; one Kafka topic = one Benzene topic, headers/value verbatim | Broker-side redelivery/DLQ — Kafka has none; a failed message goes to the `OnFailure` hook and is committed past, keeping the partition moving (halting on poison is too drastic a default) | Wire `OnFailure` to a dead-letter topic publish or log; handlers must tolerate skip-and-continue |
| **RabbitMQ** | `rabbitmq` (own module, `amqp091-go`): self-hosted `Consumer` + outbound `Client` | — | — |
| **CloudEvents** | `cloudevents/` — envelope↔CloudEvents 1.0 mapping + inbound HTTP handler (both content modes), zero-dep | Ship per-broker CloudEvents transports — the mapping is the bridge; delivery is whatever CloudEvents-shaped system you point at it | Point Event Grid, Knative, EventBridge, etc. at the handler |

## AWS

Full parity with the .NET reference on cloud integrations (see `PARITY.md`).

| Area | What benzene-go provides | What it deliberately does NOT do (and why) | How to solve the rest |
|---|---|---|---|
| **Lambda runtime + HTTP** | `awslambda/` — hand-rolled Runtime API bootstrap; HTTP adapter for Function URL / API Gateway v2, REST/v1, ALB (shape-detected per invocation), plus envelope adapter | The API Gateway custom-authorizer sub-feature is **not implemented** | Attach an authorizer at the gateway; claims arrive as request context you can read in middleware |
| **SQS** | `awssqs` (own module): inbound Lambda `Handler` (per-message batch-item failure) + outbound `Client` + self-hosted `Consumer` poller | Hide SQS specifics — the native record is exposed | Use SQS features (delay, FIFO, attributes) via the SDK directly |
| **SNS / EventBridge / S3** | `awssns`, `awseventbridge` (own modules — inbound handlers + outbound clients), `awss3` (root module, inbound) | Batch/partial-failure reporting on these — the platform offers none for direct async invokes; a failure returns a Go error so AWS's async-invoke retry applies (the same default as .NET's 1.0 settlement contract; per-binding detail in the settlement breakdown below) | Handlers must be idempotent (at-least-once) |
| **DynamoDB Streams / Kinesis / MSK Kafka** | `awsdynamodb/`, `awskinesis/`, `awskafka/` (root module, zero-dep) — ordered CDC/stream handlers: sequential per partition, stop at first failure, report the resume position via batch-item failures | Concurrent fan-out on ordered streams — ordering is the point of a stream; and no outbound side (writing the table/stream *is* the publish) | Configure `ReportBatchItemFailures` on the event source mapping |
| **Outbound invoke / orchestration** | `awslambdaclient` (Lambda invoke, RequestResponse/Event), `awsstepfunctions` (StartExecution, idempotent name) — own modules | Run/resume workflows — starting an execution is Benzene's job; the workflow is Step Functions' (see Saga row) | Model durable workflows in Step Functions itself |
| **X-Ray** | Covered via `diagnostics` (OTel → X-Ray via ADOT/OTLP) | A direct X-Ray-SDK port of `.Aws.Lambda.XRay` — **not implemented**; the OTel path is the intended route | Export OTLP to ADOT |

## Azure

Custom-handler model: the Functions host POSTs a `Data`/`Metadata` JSON envelope; parity turns on
whether a trigger reduces to that envelope (see `PARITY.md` §Azure).

| Area | What benzene-go provides | What it deliberately does NOT do (and why) | How to solve the rest |
|---|---|---|---|
| **Functions triggers** | `azurefunctions/` (zero-dep): HTTP `Handler`, `QueueHandler` (Storage Queue + Service Bus, topic resolution + outer-500 redelivery), `CosmosHandler` (fan-in change-feed batches), `TimerHandler`, `EventGridHandler` (Event Grid + CloudEvents schema), `EventHubHandler` (batch cardinality, per-event routing) | **Blob trigger: deferred by design** — it is SDK-typed (`BlobClient`) in .NET, not a JSON the custom handler forwards; a faithful port must own the lease via `azblob`, and this repo does not fabricate an unverifiable custom-handler shape. **Functions Kafka trigger: not implemented** — zero-dep-achievable, but the exact custom-handler payload must first be pinned against a live host (same no-fabrication rule). Service Bus *trigger* settle is outer-status only (≈ AutoComplete) — explicit per-message settle needs the SDK | For blobs, react to the Event Grid `BlobCreated` event via `EventGridHandler` and read the blob with `azblob` in your handler; for explicit Service Bus settle, use the self-hosted worker below |
| **Self-hosted workers** | `azureservicebus.Worker` (explicit complete/abandon/dead-letter), `azureeventhub.Consumer` (caller-owned checkpoint), `azurecosmos.Worker` (caller-owned lease, fan-in) — own modules | Own the Event Hub checkpoint store / Cosmos lease for you — checkpoint strategy is an application decision the worker surfaces rather than hides | Wire the SDK's checkpoint/lease store of your choice |
| **Outbound clients** | `azureservicebus.Client`, `azureeventhub.Client`, `azureeventgrid.Client`, `azurequeuestorage.Client` — own modules | — | — |

## GCP

| Area | What benzene-go provides | What it deliberately does NOT do (and why) | How to solve the rest |
|---|---|---|---|
| **Cloud Functions Gen2** | `gcpfunctions` (own module): `RegisterHTTP` + `RegisterCloudEvent` (functions-framework) | — | The zero-dep Cloud Run path needs no package at all — mount `httpbinding` |
| **Pub/Sub** | `gcppubsub/` — zero-dep push-subscription `http.Handler` (ack/nack via status code); `gcppubsubclient` (own module) — outbound publish | Pull subscriptions on the zero-dep path — pull needs the SDK's streaming client; push is the zero-dep shape | Use `gcpfunctions.RegisterCloudEvent` or run the SDK's pull client feeding the pipeline |

## Mesh, health, spec surface

| Area | What benzene-go provides | What it deliberately does NOT do (and why) | How to solve the rest |
|---|---|---|---|
| **Mesh service-side** | `mesh/` — `Descriptor` derived from the live `Registry` (topics + startup-derived JSON Schemas + `descriptorHash`), reserved-`benzene:mesh` descriptor middleware, `TraceMiddleware` + outbound `WithTraceContext`, `LogExporter`/`PushExporter` trace feeds, issue emitter (`IssueMiddleware`/`PushIssueExporter`) — every feed independent and optional | Capture a language-native `exceptionType` on issues — the Go router converts a panic to a `service-unavailable` result before middleware sees it (the field is optional in mesh.md §4.1 for exactly this) | Status-based classification carries the signal |
| **Mesh collector** | `meshd/` — register/heartbeat/traces/issues ingest + `benzene:mesh:query:*` read models over an in-memory store with a bounded trace ring (`meshd/meshd.go`, `store.go`); wire contract pinned by vendored `mesh-*.json` fixtures | Durable storage — the collector is a diagnostic surface, not a system of record. The produced-vs-consumed version-skew read model is **not implemented** | Any port's collector can host this port's services over the shared `benzene:mesh:*` contract |
| **Mesh View (UI)** | `meshd.ViewHandler` (`meshd/view.go`) — serves a **Go-native** embedded page (`view.html`, no JS framework, no external assets) at `/benzene/fleet-ui`, polling the collector's fleet query through the envelope endpoint. It does **not** embed or serve a canonical cross-language `mesh-ui.html` — no such asset is referenced in this repo; the page is this port's own rendering of the shared query contract | A rich SPA — zero-dependency stance extends to the page | The wire contract is the interop point; any UI can poll `benzene:mesh:query:*` |
| **Health checks** | `healthcheck/` — reserved-topic interception middleware + `TCPCheck`, `HTTPPingCheck`, `DiskSpaceCheck` (zero-dep; error *categories*, not raw messages, so internal hostnames/credentials don't leak) | Ship dependency-specific checks (database ping, broker liveness) — a check is a one-function interface | Implement `healthcheck.Check` calling your dependency's SDK |
| **Client-side contract check** | `clienthealthcheck.ServiceCheck` — probes a provider's `benzene:mesh` descriptor via an outbound `Sender`, reporting the *contract* relationship (drift = `warning`, not a health flip); expected hash supplied via `WithExpectedContractHash` | Couple the check to the provider's transient health — the descriptor is served health-independently on purpose, so contract drift and downtime stay distinguishable. (.NET bakes the hash into a generated client; Go supplies it explicitly — a documented divergence) | Use `benzene:healthcheck` for liveness; this check is a contracts diagnostic |
| **Spec endpoint & profile** | `mesh.SpecHandler` (R5); `cloudservice.New` — one-call Cloud Service Profile builder wiring R1–R5/R7 + descriptor, returning an honest `ProfileReport` (R1–R8 checklist); `cloudserviceprobe.Run` — external black-box R1–R8 probe with tri-state verdicts | `cloudservice.New` does not wire R6 (collector feeds) or R8 (trace propagation) — they need a collector and exporter lifecycle the app owns; the report says so rather than overclaiming. The probe keeps its own path constants — it must audit *any* port's service, so it deliberately duplicates rather than imports | `Unsatisfied()` on the report is the exact to-do list; wire `mesh.TraceMiddleware`/`WithTraceContext` and the push exporters yourself |
| **Contract docs & codegen** | `openapi/` + `asyncapi/` — OpenAPI 3.0 / AsyncAPI 3.0 generation from the mesh descriptor, each with a GET `Handler`; `codegen` (own module: `contractdoc` + `gengo` + `cmd/benzene-codegen`) — typed-client generation from a committed Contract Document per the cross-language `contract-document.md` spec | Fabricate a sync-vs-event classification — the descriptor can't carry the send side, so AsyncAPI `send` operations are caller-declared (`WithSentEvent`). Deriving them from `responseevents` mappings is **not implemented** | Declare sent events by hand; see `docs/codegen-client.md` for the client pipeline |

## Cross-cutting building blocks

| Area | What benzene-go provides | What it deliberately does NOT do (and why) | How to solve the rest |
|---|---|---|---|
| **Idempotency** | `idempotency/` — atomic-claim middleware over a pluggable `Store`; `InMemoryStore` (single-process, TTL); store outage fails open; a failure is never permanently suppressed | Cross-instance de-duplication — independent processes can't coordinate without external shared state, and shared state can relocate the race, not remove it | An external store with an atomic conditional write (DynamoDB conditional put, Redis `SET NX`) behind the `Store` interface, plus naturally idempotent handlers |
| **Settlement of a returned failure result** | Every one-way inbound binding acknowledges a message only when the invocation's success flag is true (`envelope.DispatchResult`/`DispatchTopicResult`). A failure result, **no result** (reported as `unexpected-error`), an **unmatched topic** (`RouterMiddleware` sets `not-found`), a middleware's Go error and a recovered handler panic all count as failures and are left for the transport to redeliver — with two exceptions: the self-hosted `kafka.Consumer` commits past them by design (skip-and-continue), and `awslambda.EnvelopeHandler` acknowledges them when invoked asynchronously. The self-hosted `azureeventhub.Consumer` is written to redeliver but skips in a running process. See the [per-binding breakdown](#returned-failure-result-settlement--the-per-binding-breakdown) | A single cross-transport acknowledgement abstraction — each binding settles with its transport's own signal (batch-item failure, Go error, non-2xx, nack, abandon, withheld delete/commit/checkpoint). **Not implemented:** an at-most-once override on any binding except RabbitMQ (`NoRequeue`); an option to treat an unmatched topic as `ignored`; an at-least-once mode for `kafka.Consumer` | Make handlers idempotent (`idempotency/`). To acknowledge an outcome on purpose (at-most-once, or `not-found` on a shared-bus subscriber), add an outermost middleware that rewrites `ic.Result` to `benzene.Ignored` after `next` returns. Bound retries with the transport's redrive/DLQ settings |
| **Outbox** | **Not implemented.** Nothing ships; the only trace is a seam — `responseevents.Publisher` is documented as implementable by "an outbox relay" (`responseevents/responseevents.go`). This is an unbuilt gap, not a design decision: the .NET reference ships `Benzene.Outbox`, and this port's ROADMAP records no "no" for it | — | Store-and-forward in your handler's own transaction against your store, relaying via a `client.Sender`; pair the consuming side with `idempotency` |
| **Oversized payloads (claim check)** | **Not implemented.** No package, no seam. An unbuilt gap, not a design decision — the .NET reference ships `Benzene.ClaimCheck` | — | Offload to blob storage in a small middleware pair (offload before send, hydrate before deserialize), carrying the reference in a header |
| **Resilience** | `resilience/` (zero-dep): retry (full-jitter backoff, `WithRetryOnError`/`WithRetryOnResult`), cooperative `Timeout`, `Bulkhead`, `Fallback`; `circuitbreaker` (own module, `sony/gobreaker/v2`) with `WithTripOnResult` | Forcibly stop a ctx-ignoring handler — impossible in Go; `Timeout` bounds the *wait* cooperatively. **Hedging is not implemented** — racing attempts needs per-attempt `InvocationContext` isolation the shared-`ic` pipeline contract doesn't provide, a core-concepts question deliberately not forced package-locally | Honor `ctx` in handlers; for hedging, race at the caller (your own goroutines) until the contract question settles |
| **Rate limiting** | `ratelimiting/` — per-instance middleware over a `Limiter` interface + `TokenBucket` default; rejection = `too-many-requests` | Fleet-wide (authoritative) limiting — N instances admit up to N× the rate; authoritative limiting belongs at the gateway | Limit at the gateway/load balancer, or plug a shared-store `Limiter` |
| **Caching** | `cache/` — pluggable `Store` + read-through `GetOrLoad[T]`; `InMemoryStore` default; degrades safely (read error = miss, write error ignored) | Ship a distributed cache — a shared store is its own module/dependency decision | Implement `Store` over Redis/Memcached |
| **Sagas** | `saga/` — in-process staged orchestrator with LIFO compensation, `SagaContext` result threading, observability `StateStore`, clean-rollback-only `RetryPolicy` | Durable crash-resume — steps are closures that can't be rehydrated; the state store records progress for *observability*, not recovery (matching `Benzene.Saga`'s own boundary) | Use a durable orchestrator (Step Functions via `awsstepfunctions`, Durable Functions, Temporal) for crash-durable workflows |
| **Response events** | `responseevents/` — post-handler republish of the response as a follow-up event (`Map`, `CrudConvention`, fan-out mappings; `FailMessage` default so a lost publish is never silently swallowed) | The .NET package's build-time unmapped-response diagnostic and mapping-derived AsyncAPI send side — **not implemented** (the `asyncapi` generator exists; deriving its send side from these mappings is the open, unbuilt piece) | Declare sent events via `asyncapi.WithSentEvent`; use `LogAndContinue` + `OnPublishError` where publish loss is acceptable |
| **AuthN / AuthZ** | `auth/` (zero-dep): `Principal`, `BasicAuth` (RFC 7617), `BearerAuth` — stdlib JWT validation with a strict algorithm allowlist (RFC 8725), per-family typed keys, iss/aud/exp/nbf/iat + skew; keys from `StaticKeys` or a caching `JWKSResolver` with OIDC discovery (`auth/jwks.go`: `NewJWKSFromAuthority`); `Authorize`/`RequireRole`/`RequireScope` | Ship a policy-engine (OPA/Cedar) adapter; leak validation failure detail to callers (the real reason reaches only `WithOnError`) | An authorization middleware calling your policy engine, built on `Principal` |
| **Observability** | `diagnostics` (own module, OTel **API** only): one server span per invocation, W3C traceparent join, count/duration metrics, outbound `WithTraceContext`; `logging/` — zero-dep `log/slog` per-invocation line | Own the OTel SDK/exporter or ship vendor packages (Datadog, Zipkin) — the application owns the SDK; standard OTLP export covers vendors | Configure the OTel SDK + OTLP exporter in `main` |
| **Database / state access** | *(nothing — by design)* | Any database/state-store abstraction — a database is not a transport; wrapping one hides its capabilities (the core anti-pattern) | Your handler uses its own driver/SDK directly |
| **Conformance** | `conformance/` — runs this port against the vendored language-neutral fixtures from the main repo's `docs/specification/conformance/` | Edit a fixture to make this port pass — the fixture is the neutral truth | A mismatch is a bug here or a deliberate spec change there, never a fixture patch |

## Returned-failure-result settlement — the per-binding breakdown

Audited against the source on 2026-10-11. "Failure result" means the pipeline ended with a result
whose success flag is false (`validation-error`, `not-found`, …). Every row inherits four facts:

- Every binding except `awslambda.EnvelopeHandler` settles on the success flag from
  `envelope.DispatchResult`/`DispatchTopicResult` (`envelope/dispatch.go`). That flag is false for a
  failure result, for **no result** (reported as `unexpected-error`), and for a middleware's returned
  Go error (`service-unavailable`). So the *no result* column always matches the *failure result*
  column.
- `RouterMiddleware` (`router.go`) always sets a result: an unmatched topic is `not-found`, an empty
  topic is `validation-error`, and a handler panic is recovered as `service-unavailable`. No binding
  special-cases any of them, so *unmatched topic* also matches *failure result*. No binding offers to
  treat an unmatched topic as `ignored`.
- `Pipeline.Run` (`middleware.go`) does **not** recover a panic in a middleware outside the router.
  The *uncaught error* column records where that panic lands.
- `redeliver` means the binding withholds acknowledgement (batch-item failure, Go error, non-2xx or
  no response, nack, abandon, or no delete/commit/checkpoint), so the transport's own redelivery
  applies. Retry counts and dead-lettering are the transport's redrive configuration, not Benzene's.

| Binding | Package / file | Failure result | No result | Unmatched topic | Uncaught error (middleware panic) | Override knob | Unit of settlement |
|---|---|---|---|---|---|---|---|
| Lambda SQS trigger | `awssqs` — `sqs.go` `Handler` | redeliver (`batchItemFailures`) | redeliver | redeliver | redeliver — the runtime recovers it (`awslambda/runtime.go` `invoke`) and fails the invocation, so the whole batch is retried | none | per message (`messageId`; needs `ReportBatchItemFailures` on the mapping) |
| SQS self-hosted poller | `awssqs` — `consumer.go` `Consumer.poll` | redeliver (not deleted; visibility timeout) | redeliver | redeliver | redeliver — panics out of `Run` (not recovered); nothing in the received batch is deleted | none (`OnFailure` only observes) | per message (`DeleteMessageBatch` of the successes) |
| Lambda SNS | `awssns` — `sns.go` `Handler` | redeliver (Go error → async-invoke retry) | redeliver | redeliver | redeliver — invocation fails | none | per invocation (one notification) |
| Lambda EventBridge | `awseventbridge` — `eventbridge.go` `Handler` | redeliver (Go error) | redeliver | redeliver | redeliver — invocation fails | none | per event |
| Lambda S3 | `awss3` — `s3.go` `Handler` | redeliver (Go error; stops at the first failed record) | redeliver | redeliver | redeliver — invocation fails | none | per invocation (the whole event is re-run) |
| Lambda DynamoDB Streams | `awsdynamodb` — `dynamodb.go` `Handler` | redeliver from the failed record | redeliver | redeliver (the shard is retried from that record) | redeliver — invocation fails (whole batch) | none | per shard position (stop at first failure, `SequenceNumber`) |
| Lambda Kinesis | `awskinesis` — `kinesis.go` `Handler` | redeliver from the failed record | redeliver | redeliver (the shard is retried from that record) | redeliver — invocation fails (whole batch) | none | per shard position (stop at first failure, `SequenceNumber`) |
| Lambda MSK / self-managed Kafka | `awskafka` — `kafka.go` `Handler` | redeliver from the failed offset | redeliver | redeliver (that partition is retried from that offset) | redeliver — invocation fails (whole batch) | none | per partition position (`{partition, offset}`; partitions independent) |
| Lambda direct invoke, async (`InvocationType: Event`) | `awslambda` — `envelope.go` `EnvelopeHandler` | acknowledged (dropped) — the failure envelope is returned as a successful invocation | acknowledged (dropped) | acknowledged (dropped) | redeliver — invocation fails (async retry) | none | per invocation (n/a under `RequestResponse`: the caller reads the envelope) |
| Kafka self-hosted consumer | `kafka` — `kafka.go` `Consumer.Run` | skip-and-continue (`OnFailure`, then committed) | skip-and-continue | skip-and-continue | redeliver on restart — panics out of `Run`; offset uncommitted | none (`OnFailure` can publish to a dead-letter topic but cannot stop the commit) | per message (commit after each record) |
| RabbitMQ consumer | `rabbitmq` — `consumer.go` `Consumer.handle` | redeliver once (`Nack` with requeue), then nack without requeue (dead-letter exchange, or dropped) | same | same | redeliver — panics out of `Run`; the broker requeues the unacked delivery when the channel closes | `NoRequeue` (nack without requeue on the first failure); no auto-ack mode | per delivery |
| Pub/Sub push | `gcppubsub` — `gcppubsub.go` `Handler` | redeliver (HTTP 500) | redeliver | redeliver | redeliver — no response (`net/http.Server` recovers and closes the connection) | none | per message |
| Cloud Functions Gen2 CloudEvent | `gcpfunctions` — `gcpfunctions.go` `dispatchCloudEvent` | redeliver² (returns an error) | redeliver² | redeliver² | redeliver² — functions-framework v1.9.2 re-panics; cloudevents sdk-go v2.16.2 (`client/invoker.go`) recovers it into an error result | none (`WithOnFailure` only observes) | per event |
| CloudEvents over HTTP | `cloudevents` — `http.go` `Handler` | redeliver (HTTP 500) | redeliver | redeliver | redeliver — no response (`net/http`) | none | per event (batched mode answers 415) |
| Functions Storage Queue / Service Bus trigger | `azurefunctions` — `queue.go` `QueueHandler` | redeliver (outer 500) | redeliver | redeliver | redeliver — no response (`net/http`) | none | per message (invocation) |
| Functions Event Grid trigger | `azurefunctions` — `eventgrid.go` `EventGridHandler` | redeliver (outer 500) | redeliver | redeliver | redeliver — no response (`net/http`) | none | per event |
| Functions Event Hub trigger | `azurefunctions` — `eventhub.go` `EventHubHandler` | redeliver² (outer 500; stops at the first failed event) | redeliver² | redeliver² | redeliver² — no response (`net/http`) | none | per batch (invocation) |
| Functions Cosmos DB change feed | `azurefunctions` — `cosmos.go` `CosmosHandler` | redeliver (outer 500, whole batch) | redeliver | redeliver (the code-named topic has no handler) | redeliver — no response (`net/http`) | none | per batch (fan-in: one invocation) |
| Functions Timer | `azurefunctions` — `timer.go` `TimerHandler` | n/a — invocation marked failed (outer 500); a tick is never redelivered | n/a (same) | n/a (same) | n/a — no response | none | per tick |
| Service Bus self-hosted worker | `azureservicebus` — `worker.go` `Worker.settle` | redeliver (`AbandonMessage`) | redeliver | redeliver | redeliver — panics out of `Run`; the lock expires | `AckMode`: `AckModeAbandon` (default) or `AckModeDeadLetter` (dead-letter on the first failure); no complete-on-failure mode | per message |
| Event Hub self-hosted consumer | `azureeventhub` — `consumer.go` `Consumer.receiveAndDispatch` | skip-and-continue¹ in a running process | skip-and-continue¹ | skip-and-continue¹ | redeliver on restart — panics out of `Run`; not checkpointed | none (`OnFailure` only observes; `Checkpoint` is caller-owned) | per event (the caller's `Checkpoint` runs after each success; a batch stops at its first failure) |
| Cosmos DB self-hosted worker | `azurecosmos` — `worker.go` `Worker.pollAndDispatch` | redeliver (token not advanced; the same page is re-read after `ErrorBackoff`, indefinitely) | redeliver | redeliver (the code-named topic has no handler) | redeliver on restart — panics out of `Run` | none (`OnFailure` only observes; `Checkpoint` is caller-owned) | per page (continuation token; fan-in) |

¹ `receiveAndDispatch` stops at the first failure without checkpointing and returns `nil`, and `Run`
receives again. The SDK-backed receiver (`NewPartitionReceiver`) does not rewind: azeventhubs
v2.0.2's `PartitionClient.ReceiveEvents` moves its read position past the whole received batch. So in
a running process the failed event is not re-read, the rest of its batch is never dispatched, and the
next successful event's `Checkpoint` moves the durable position past both. The failed event is
redelivered only if the process restarts from the durable checkpoint before that happens. The type's
doc comment ("the failed event redelivers on resume") covers only that restart case.
`TestConsumer_StopsAtFirstFailureInBatch` tests one cycle, not the next.

² The binding fails the invocation. Whether the platform then redelivers depends on the trigger's
retry configuration (`gcpfunctions`' own doc says "subject to the trigger's retry policy"), which this
repo cannot verify. In particular, how the Functions host checkpoints an Event Hub batch after a
failed invocation is unverified.

Not listed: the request-response bindings (`httpbinding`, `awslambda.HTTPHandler`,
`azurefunctions.Handler`, `grpcbinding`, `gcpfunctions.RegisterHTTP`). There a failure result is the
caller's response, so settlement is n/a. The Functions Kafka and Blob triggers and Pub/Sub pull are not
implemented (see the rows above).

### Divergences from the .NET 1.0 settlement contract

Same default as .NET (a failure result or no result is redelivered): the SQS trigger and poller, SNS,
EventBridge, S3, DynamoDB Streams, Pub/Sub, the Storage Queue / Service Bus / Event Grid triggers, the
Service Bus worker, and RabbitMQ (including the single bounded requeue). The differences:

- **No at-most-once override on most bindings.** .NET gives every binding an opt-out
  (`RaiseOnFailureStatus = false`, an `AckMode`, `BatchFailureMode`). Here only RabbitMQ has one:
  `NoRequeue` nacks without requeue, so the message is dead-lettered, or dropped when no dead-letter
  exchange is configured. The Service Bus worker's `AckModeDeadLetter` quarantines a message; it does
  not acknowledge it. This is not implemented, not a design decision.
- **Unmatched topic on `awskafka` (MSK) and the Functions Event Hub trigger.** .NET acknowledges an
  unrouted (no-result) outcome on these, because they have no per-record dead-letter path. Here the
  router always sets `not-found`, so these bindings redeliver: MSK retries the partition from that
  offset, and the Event Hub trigger fails the whole batch. This port has no "unrouted = no result"
  outcome. (.NET's third such exception, the Functions Kafka trigger, has no counterpart here.)
- **Self-hosted Kafka (`kafka.Consumer`).** The default matches .NET: at-most-once, with unmatched
  topics committed past. There is no at-least-once mode to match .NET's `CommitOnlyOnSuccess = true`;
  `OnFailure` is the only lever.
- **Self-hosted Event Hub (`azureeventhub.Consumer`).** .NET defaults to at-most-once and offers
  `CatchHandlerExceptions = false` for at-least-once. This consumer is written to be at-least-once but
  skips in a running process (footnote ¹). Unlike .NET, it also never dispatches the rest of the
  failed event's batch. There is no knob either way.
- **Cosmos DB change feed (`CosmosHandler`, `azurecosmos.Worker`).** In .NET a returned result does
  not apply (fan-in, with no result; only a throw redelivers). Here the batch is one result-bearing
  invocation, so a failure result, no result, or a code-named topic with no handler redelivers the
  whole batch. The worker re-reads it indefinitely, with no skip option to match .NET's
  `CatchHandlerExceptions = true`.
- **Kinesis (`awskinesis`).** .NET is fan-in with handler-driven checkpoints. Here each record is
  routed by stream name, and processing stops at the first failure. Both redeliver, but the unit
  differs.
- **Timer (`TimerHandler`).** The outcome is the same: the failure is visible and never redelivered.
  There is no option to accept a failure result, as .NET's `RaiseOnFailureStatus = false` does.
- **Lambda direct invoke, async (`awslambda.EnvelopeHandler`).** A failure result, no result and an
  unmatched topic are all acknowledged under `InvocationType: Event` (for example from
  `awslambdaclient` in `Event` mode). .NET's breakdown has no counterpart.
- **Middleware panic on a self-hosted worker.** .NET's Service Bus and RabbitMQ workers
  abandon or nack a throwing message and carry on. Here the panic escapes `Run`. The in-flight message
  is redelivered by the broker, or from the uncommitted position after a restart.

## Deliberately out of scope (a "no", not a gap)

.NET-ecosystem idioms with no Go analogue to port (see `ROADMAP.md` for the full reasoning):
alternate DI containers (`Container`/`Scope` is the Go idiom), alternate loggers (`log/slog` is
standard), alternate serializers (`encoding/json` is idiomatic; Avro/MessagePack would be new
dependency decisions, not ports), vendor-specific observability packages (OTLP covers them), and
ASP.NET-on-Lambda/Functions hosting bridges (`net/http` handlers *are* the pipeline here).

## Why "we don't do that" is a feature

Every deliberate "no" above buys you something: you keep the full power of the tool you chose,
you're never blocked by a leaky abstraction, and the zero-dependency core stays small and stable.
When you need something benzene-go doesn't ship, the extension model — a custom `Middleware`, a
custom `Check`, a `Store`/`Limiter`/`Sender` implementation — is the supported, documented path.
See `docs/middleware.md`.
