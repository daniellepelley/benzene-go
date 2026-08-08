# benzene-go roadmap

What exists, what's next, and what's deliberately out of scope. This isn't a promise of
delivery order - just the current honest picture, kept up to date as things land.

## Done

- Core spec model: `Topic`, `Status`, `Result[T]`/`ResultInfo`, `Registry`, `Middleware`/
  `Pipeline`, `RouterMiddleware`, the DI-lite `Container`/`Scope` (+ `ScopeFromContext` for
  handler-level resolution), the three-phase `App` lifecycle.
- `wire` - the transport-neutral envelope, plus `ResolveMetadataTopic` (the shared native-metadata
  topic resolver every queue-shaped binding delegates to) and `ReservedNames` - the single
  injectable value for the configurable reserved names of `wire-contracts.md` §2 (the `topic`
  metadata key and the `x-correlation-id` header). A service sets it once on the
  `ApplicationBuilder` (`UseReservedNames`) for its inbound bindings and on its outbound clients'
  `ReservedNames` field / `CorrelationDecoratorWithKey`, so a colliding key is renamed in one
  place for both directions; the defaults carry the interop baseline.
- `httpstatus` - the Benzene<->HTTP status mapping tables (conformance-verified).
- `grpcstatus` - the Benzene<->gRPC status mapping tables (conformance-verified), in raw
  numeric gRPC status codes so it stays zero-dependency like `httpstatus`.
- `envelope` - `wire.Request` -> `Pipeline` -> `wire.Response` dispatch, shared by every
  HTTP-shaped binding below.
- `httpbinding` - native REST-style HTTP binding + envelope-over-HTTP.
- `httpclient` - the HTTP outbound client (one `Send` method).
- `healthcheck` - reserved-topic health-check interception middleware.
- `validation` - request-validation building block (zero dependencies): `Validated(validator,
  handler)` wraps a handler so an invalid request short-circuits to a `validation-error` result
  before the handler runs, plus `Validator[T]`/`ValidatorFunc[T]` and a `Combine` composer. The
  Go-idiomatic form of `Benzene.DataAnnotations`/`Benzene.FluentValidation`'s ValidationMiddleware -
  a typed handler wrapper rather than a pipeline middleware, because this port's pipeline is
  type-erased until dispatch (no reflection, no struct-tag DSL).
- `idempotency` - de-duplicates redelivered messages on an at-least-once transport (zero
  dependencies), matching `Benzene.Idempotency`: a pipeline `Middleware(store, key)` that atomically
  claims a header-derived key in a pluggable `Store` and runs the handler only the first time - a
  completed duplicate is `ignored` (ack), an in-progress one is `conflict` (retry), and the winning
  attempt records completion on success or releases on failure (a failure is never permanently
  suppressed). `InMemoryStore` (TTL + injectable clock) is the default; a store outage fails open.
- `ratelimiting` - best-effort per-instance rate-limiting middleware (zero dependencies), matching
  `Benzene.RateLimiting`: `Middleware(limiter, cost)` acquires each message's permit cost from a
  `Limiter` and short-circuits a rejected message to `too-many-requests`. The .NET package uses
  `System.Threading.RateLimiting`; this stays dependency-free with a `Limiter` interface + a
  standard-library `TokenBucket` default (plug a different algorithm behind the interface). Per
  instance, so a fleet of N admits up to N× the rate - authoritative limiting belongs at the gateway.
- `resilience` - retry middleware (zero dependencies), matching `Benzene.Resilience` (which is
  retry-ONLY: circuit breaker/timeout/hedging/fallback live in the Polly-backed sibling, deferred
  here pending a dependency decision). `Middleware(opts...)` re-invokes the downstream pipeline with
  exponential backoff. The Go router funnels application failures onto `ic.Result` (not a Go error),
  so retry has two triggers mirroring .NET's `shouldRetry`/`shouldRetryContext`: `WithRetryOnError`
  (default: any error except context cancellation) and `WithRetryOnResult` (default: never; the lever
  services set - `RetryUnsuccessful` / `RetryOnStatus(...)`). Backoff caps and jitters the sleep while
  growing the exponential curve uncapped (AWS "full jitter", `FullJitter` helper), with a
  context-cancellable sleep and an injectable `WithSleep` for tests.
- `auth` - authentication/authorization building block (zero dependencies), matching
  `Benzene.Auth.Core`+`.Basic`: a `Principal` (name/roles/claims) threaded on the context,
  `BasicAuth(validate, realm)` RFC 7617 authentication middleware (validates via an app-supplied
  `BasicValidator` - no default credential - and short-circuits `unauthorized` with a
  `WWW-Authenticate` challenge, or sets the principal), and `Authorize(predicate)`/`RequireRole(role)`
  authorization middleware (`forbidden` when not permitted). Header-based; authentication is for
  HTTP-fronted pipelines.
- `cache` - caching building block (zero dependencies), matching the essence of `Benzene.Cache.Core`:
  a pluggable `Store` (Get/Set/Delete with TTL) + a generic read-through helper `GetOrLoad[T]` (the
  Go form of `CacheEntry.LazyLoad`). `InMemoryStore` (thread-safe, TTL + clock) is the default; a
  shared store is its own module. Degrades safely (read error = miss, write error ignored, load
  error returned and not cached).
- `logging` - basic request logging/timing middleware using only `log/slog`: one structured
  line per invocation (topic/version, Benzene status, duration; Info/Warn/Error by outcome).
  The dependency-free visibility option alongside the `diagnostics` module's full OTel feed.
- `awslambda` - AWS Lambda binding (hand-rolled Runtime API bootstrap, HTTP + envelope
  adapters; the HTTP adapter handles Function URL / API Gateway v2.0, API Gateway
  REST/v1.0, and ALB target-group event shapes, detected per invocation).
- `azurefunctions` - Azure Functions custom-handler binding: `Handler` for HTTP-triggered
  functions, `QueueHandler` for queue-shaped triggers (Storage Queue, Service Bus) with
  wire-contracts §2 topic resolution and platform-native retry on failure, and `CosmosHandler`
  for the Cosmos DB Change Feed trigger (the `Benzene.Azure.Function.CosmosDb` flavor of
  `transport-bindings.md`'s "Cosmos DB Change Feed" entry). The change-feed binding is
  **fan-in, not topic-routed** (core-concepts §3, streaming-shaped): the Functions host owns the
  change-feed connection and lease container and forwards each delivered batch of changed
  documents over the same Data/Metadata envelope, so the handler is zero-dependency; the whole
  batch is one pipeline invocation (not one per document) dispatched to the topic the developer
  names, whose handler receives the batch as a slice (`Handler[[]TDocument, TRes]`). A
  `TimerHandler` covers the Timer trigger the same fan-in way (a scheduled tick has no message, so
  the topic is named in code, the body is the tick's schedule info, outer 200/500, no redelivery).
  Checkpointing is batch-level and on successful return only, so a non-success dispatch answers
  outer HTTP 500 and the host redelivers the entire batch - the same outer-status convention as
  `QueueHandler`. The version-aware fan-in rides on `envelope.DispatchTopicResult` (explicit
  programmatic dispatch to a named, possibly-versioned topic - distinct from reading a version
  header off an inbound message, which no binding does; see the versioning note below).
- `client` - outbound-client decorators (`CorrelationDecorator`, `RetryDecorator`) over a
  transport-agnostic `Sender` interface; `httpclient.Client` satisfies it structurally. The
  spec's third cross-cutting client behavior, trace-context propagation, is
  `mesh.TraceContextDecorator` - it lives in `mesh` (which owns the `Span` it forwards) so
  `client` stays free of a mesh dependency.
- `cors` - portable CORS middleware for HTTP-fronted services (origin/scheme/port matching,
  header wildcard, preflight handling), a Go port of the main repo's own portable CORS
  middleware.
- `benzenetest` - in-process test host (`Invoke[TReq, TRes]`) for a consuming application's own
  tests, the Go counterpart to `Benzene.Testing`/`BenzeneTestHost`.
- `awssqs` - AWS SQS binding, in its **own Go module** (see `RELEASING.md`): an inbound
  `Handler` for a Lambda triggered by an SQS event source mapping (zero dependencies - hand-
  rolled JSON, like `awslambda`), and an outbound `Client` that publishes via `SendMessage`
  (needs `aws-sdk-go-v2/service/sqs` - this repo's first third-party dependency, isolated to
  just this module).
- `awssns` - AWS SNS binding, in its **own Go module** (see `RELEASING.md`): an inbound
  `Handler` for a Lambda subscribed directly to an SNS topic (zero dependencies), and an
  outbound `Client` that publishes via `Publish` (needs `aws-sdk-go-v2/service/sns`, isolated to
  just this module). Unlike SQS's event source mapping, a direct SNS-to-Lambda subscription has
  no batch/partial-failure mechanism - `Handler` instead returns a Go error for a failed
  notification, triggering AWS's own async-invoke retry.
- `mesh` - Phases 1-2 of `docs/design/mesh.md`: the service `Descriptor` derived from the live
  `Registry` (topics + startup-derived JSON Schemas + `descriptorHash`),
  reserved-`benzene:mesh`-topic descriptor middleware, `TraceMiddleware` with W3C `traceparent`
  propagation (plus `TraceContextDecorator`, its outbound counterpart - the client decorator that
  forwards the current span's `traceparent` onto outbound calls, so a collector derives
  who-calls-whom without a declared edge), the `LogExporter`/`PushExporter` trace feeds, and the
  issue feed's emitter
  (`IssueMiddleware` + `PushIssueExporter`: source-side classification, SHA-256 fingerprint, delta
  aggregation, liveness flush - mesh.md §4.1) - every feed independent and optional.
- `meshd` - Phases 3-4 of `docs/design/mesh.md`: the collector (register/heartbeat/traces/issues
  ingest + `benzene:mesh:query:*` read models over an in-memory store with a bounded trace ring;
  the `benzene:mesh:issues` feed merges failure signatures by fingerprint and flags the feed's
  absence only when a failure needs explaining, per mesh.md §4.1) and
  the Mesh View (one embedded self-contained page, no JS framework). The wire contract is
  promoted to the main repo's `docs/specification/mesh.md` and pinned by vendored
  `mesh-*.json` conformance fixtures.
- `cloudevents` - CloudEvents 1.0 mapping (zero dependencies): the wire envelope to/from the
  CNCF cross-cloud event format (`type` <-> topic, `data` <-> body, other attributes <->
  "ce-"-prefixed headers), plus an inbound HTTP handler for both content modes - the bridge
  that lets Event Grid, Knative, EventBridge, and anything else CloudEvents-shaped deliver
  straight into a Benzene pipeline.
- `gcppubsub` - Google Cloud Pub/Sub inbound binding (zero dependencies): an http.Handler
  for a push subscription's endpoint, with wire-contracts §2 topic resolution and ack/nack
  via the response status code. The outbound (publish) half needs the Pub/Sub SDK - see
  "Later" below.
- `awsdynamodb` - DynamoDB Streams inbound binding (zero dependencies, root module), matching
  `Benzene.Aws.Lambda.DynamoDb` and `transport-bindings.md`'s "DynamoDB Streams" entry: a Lambda
  `Handler` for a stream event source mapping. Topic is `{tableName}:{eventName}` (table parsed
  from the stream ARN + the change type); body is the record's image unmarshalled from DynamoDB
  AttributeValue format into plain JSON (NewImage, else OldImage, else Keys) so handlers
  deserialize ordinary structs; headers are `dynamodb-`-prefixed metadata. No outbound side
  (writing the table is the publish; the stream is read-only), so no SDK and no separate module.
  Records are ordered CDC, so processing is sequential and stops at the first failure, reporting
  that record's `SequenceNumber` for Lambda to checkpoint and redeliver - deliberately not
  `awssqs`'s concurrent fan-out.
- `awskinesis` - Kinesis Data Streams inbound binding (zero dependencies, root module), the direct
  sibling of `awsdynamodb` and matching `Benzene.Aws.Lambda.Kinesis`: a Lambda `Handler` for a
  stream event source mapping. Topic is the stream name parsed from the record's stream ARN (a
  Kinesis record has no per-record event type, so the stream is the routing key); body is the
  record's `data` base64-decoded into the producer's bytes (typically JSON); headers are
  `kinesis-`-prefixed metadata. No outbound side (writing the stream is the publish), so no SDK and
  no separate module. Same ordered stop-at-first-failure + first-`SequenceNumber` checkpointing as
  `awsdynamodb`.
- `awss3` - S3 event-notification inbound binding (zero dependencies, root module), matching
  `Benzene.Aws.Lambda.S3`: a Lambda `Handler` invoked by S3 on object create/remove. Topic is
  `{bucketName}:{eventName}` (bucket-qualified for consistency with `awsdynamodb`/`awskinesis`; .NET
  routes on the bare event name - the S3 topic is a local routing concern, not a wire contract);
  body is the object metadata (bucket/key/size/etag, not the contents); headers are `s3-`-prefixed.
  An S3 notification is an async invocation, so a failed record returns a Go error (async-invoke
  retry, like `awssns`) rather than a batch-item report - and deliberately not the .NET binding's
  fire-and-forget swallow, per the no-silent-drop rule. Handlers must be idempotent (at-least-once).
- `awskafka` - AWS Lambda MSK / self-managed-Kafka inbound binding (zero dependencies, root
  module), matching `Benzene.Aws.Lambda.Kafka` and DISTINCT from the self-hosted `kafka` module
  below: that one runs its own broker consumer loop (needing `segmentio/kafka-go`); this is the
  zero-dep adapter for AWS's *managed* event source mapping, which delivers records as plain JSON
  (value base64-encoded, header values as byte arrays), so it is "just" JSON parsing plus a base64
  decode with no SDK. Topic is the Kafka topic verbatim (one Kafka topic = one Benzene topic, like
  the `kafka` module - unlike Kinesis's stream-name routing); body is the record's `value`
  base64-decoded into the producer's bytes; headers pass through verbatim. Records are grouped by
  `{topic}-{partition}` and each partition is processed sequentially, stopping at its first failure
  and reporting `{partition, offset}` for that partition's resume - an **object-shaped**
  `batchItemFailures` identifier (unlike the string identifier of SQS/Kinesis/DynamoDB), needing
  `FunctionResponseTypes: [ReportBatchItemFailures]` on the mapping; partitions are independent. No
  outbound side (producing to Kafka is the publish; the trigger is read-only), so no separate module.
- `awseventbridge` - AWS EventBridge binding, in its **own Go module** (see `RELEASING.md`),
  matching the main repo's `transport-bindings.md` EventBridge entry exactly: an inbound
  `Handler` for a Lambda invoked by an EventBridge rule (zero dependencies; topic is
  `detail-type` verbatim - EventBridge's own native routing key, no bolted-on `topic`
  attribute - body is the raw `detail` JSON, and headers are `eventbridge-`-prefixed envelope
  metadata plus any wire headers embedded under the reserved `_benzeneHeaders` key inside
  `detail`, since EventBridge has no native per-message attributes; a failed event returns a
  Go error triggering AWS's async-invoke retry - the same posture as `awssns`), and an
  outbound `Client` publishing via `PutEvents` (embeds headers under `_benzeneHeaders` when
  the payload is a JSON object, mirroring `Benzene.Clients.Aws.EventBridge`; needs
  `aws-sdk-go-v2/service/eventbridge`).
- `diagnostics` - OpenTelemetry-based diagnostics middleware, in its **own Go module** (see
  `RELEASING.md`) - the Go equivalent of the main repo's `Benzene.Diagnostics`: one server
  span per invocation (topic-named, W3C traceparent join, `benzene.topic`/`benzene.status`
  attributes) plus invocation count/duration metrics, and `TraceContextDecorator` - the OTel-path
  outbound client decorator that injects the active span context as a W3C `traceparent`, the
  sibling of `mesh.TraceContextDecorator` for services observed with OpenTelemetry. Depends on the
  OpenTelemetry *API* only (`go.opentelemetry.io/otel`); the application owns the SDK and exporter,
  and standard OTLP export covers Datadog/Zipkin/etc. without vendor-specific packages (as promised
  below).
- `kafka` - Kafka binding, in its **own Go module** (see `RELEASING.md`), matching the main
  repo's `Benzene.Kafka.Core` / `transport-bindings.md` "Kafka" entry exactly: one Kafka
  topic maps to exactly one (unversioned) Benzene topic - unlike the SQS/SNS/EventBridge
  bindings, which multiplex several Benzene topics over one physical queue/bus via a header
  or `detail-type`, Kafka's own topic already is that routing key - headers pass through
  verbatim in both directions, and the message value is the body verbatim, no envelope
  wrapping. A `Consumer` loop over a consumer group (one pipeline invocation + DI scope per
  record, explicit commits; Kafka has no broker-side redelivery/DLQ, so a failed message goes
  to the `OnFailure` hook - dead-letter publish, log - and is then committed past, keeping
  the partition moving) and an outbound `Client` satisfying `client.Sender` (writes to the
  Kafka topic named after the Benzene topic, per message - mirroring
  `KafkaClientMiddleware.HandleAsync`'s `ProduceAsync(context.Topic, ...)`). Needs
  `github.com/segmentio/kafka-go` (chosen over `franz-go` for its narrow Reader/Writer
  surface, which this repo's fake-behind-an-interface test style wraps cleanly) - a broker
  wire protocol is not reasonably hand-rollable, hence the module split.
- `grpcbinding` - gRPC binding, in its **own Go module** (see `RELEASING.md`), matching the
  main repo's `Benzene.Grpc` (+ `.AspNet`)/`Benzene.Grpc.Client` and
  `transport-bindings.md`'s gRPC entry: **unary RPCs only** - streaming (client/server/duplex)
  is a documented gap, not an oversight, left for a later addition (see the package doc for
  why). `UnaryServerInterceptor` wraps an ordinary `*grpc.Server` exactly like any other
  interceptor and claims only the methods named in its `Route` table (full method path,
  case-insensitive) - unmatched methods fall through to the real generated service untouched,
  matching "the binding claims routes, it doesn't own the server" precisely; the app still
  writes and registers real protoc-generated service code (no reflection/attribute-scanning
  codegen, consistent with this repo's explicit-registration stance). Body is proto3-JSON
  bridged both directions; the `benzene-status` trailer is set unconditionally (several
  Benzene statuses collapse onto one gRPC code); an outbound `Client` satisfying
  `client.Sender` recovers the precise status from that trailer, falling back to
  `grpcstatus.FromGRPC` when a peer doesn't set one. Needs `google.golang.org/grpc` (no gRPC
  in the Go standard library) and `google.golang.org/protobuf` (proto3-JSON).
- `conformance` - runs this port against the main repo's vendored language-neutral fixtures.
- Examples: `helloworld` (plain HTTP + DI + health check), `aws-lambda-helloworld`,
  `aws-dynamodb-helloworld`, `aws-kinesis-helloworld`, and `aws-s3-helloworld` (consumer-only
  event/stream Lambdas, root module), `azure-functions-helloworld`, `gcp-cloudrun-helloworld` (no new package needed for GCP -
  see its README), `aws-sqs-helloworld` (publisher + consumer Lambdas, its own module),
  `aws-sns-helloworld` (publisher + consumer Lambdas, its own module),
  `gcp-pubsub-helloworld` (a Cloud Run service consuming a Pub/Sub push subscription),
  `mesh-helloworld` (collector + two meshed services, local-only) - each cloud example with a
  matching CI build/test path and a gated GitHub Actions deploy workflow
  (`.github/workflows/deploy-*.yml`).

Every non-test-only package sits at 100% coverage or just under it with the gap being a
documented, genuinely-unreachable defensive branch - see each package's own comments.

## Next (zero new dependencies)

Everything previously listed here (`client`, `cors`, `benzenetest`, and the `logging`
middleware) has landed - see Done above. No zero-dependency candidate is currently queued.

## Later - needs a dependency decision first

Per `CLAUDE.md`: no third-party dependency without asking first. These are real, valuable
extensions, but each needs an explicit yes on a specific dependency before starting, not a
unilateral add:

- **Pub/Sub outbound (publish) client.** The inbound half is done with zero dependencies
  (`gcppubsub` - a push subscription is just HTTPS in). Publishing needs OAuth-signed API
  calls, i.e. `cloud.google.com/go/pubsub` - the same shape as `awssqs`/`awssns`'s outbound
  clients, and like them it would live in its own module so the dependency doesn't spread.
- **Cosmos DB Change Feed self-hosted worker** (`Benzene.Azure.CosmosDb`, the non-Functions
  flavor). The Azure Functions trigger flavor already ships zero-dependency (`azurefunctions.
  CosmosHandler` - the Functions host owns the change-feed connection). A self-hosted worker
  instead opens the change feed itself and owns the lease container + checkpoint hook, which
  needs the Cosmos SDK (`github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos`) - the same
  own-module shape as `awssqs`/`awssns`'s outbound clients.
- **Google Cloud Functions Gen2 (buildpack) deploy**, as opposed to the Cloud Run path already
  documented in `examples/gcp-cloudrun-helloworld` - needs
  `github.com/GoogleCloudPlatform/functions-framework-go`, the one Google-specific dependency
  this port has avoided by targeting Cloud Run instead.
- **Richer resilience** (circuit breaker, timeout, bulkhead, hedging, fallback), the equivalent of
  `Benzene.Resilience.Polly`. The retry piece already ships zero-dependency (`resilience` - see
  Done). The rest is what .NET delegates to Polly; the Go analogue would wrap a library such as
  `github.com/sony/gobreaker` (or `failsafe-go`) behind the same middleware surface, in its own
  module so the dependency doesn't spread - the same shape as `awssqs`/`awssns`. A plain
  timeout/deadline needs no dependency at all (`context.WithTimeout`), so that slice could land in
  the root `resilience` package first if wanted.

## Deliberately out of scope (not a "later" - a "no, and here's why")

The main C# repo has ~90 packages, many of which are .NET-ecosystem idioms with no Go
equivalent to port, not gaps in this port:

- **Alternate DI containers** (`Benzene.Autofac`) - Go has no reflection-based DI culture; this
  port's `Container`/`Scope` is already the "MAY implement as an explicit registry" idiom
  `core-concepts.md` §8 describes for languages like Go.
- **Alternate loggers** (`Benzene.Serilog`, `Benzene.Log4Net`, `Benzene.Microsoft.Logging`) -
  Go's `log/slog` (standard library since Go 1.21) is the idiomatic choice; there is no
  logging-framework-plurality problem to solve here the way .NET has one.
- **Alternate serializers** (`Benzene.MessagePack`, `Benzene.Avro`, `Benzene.Xml`,
  `Benzene.NewtonsoftJson`) - `encoding/json` is idiomatic Go; a second JSON library has no
  purpose, and Avro/MessagePack/XML support can be added later as its own package *if* a
  concrete need shows up, same as any other dependency decision.
- **Vendor-specific observability** (`Benzene.Datadog`, `Benzene.Zipkin`) - if/when this port
  gets an OpenTelemetry-based diagnostics package, standard OTLP export covers these vendors
  without a vendor-specific package each.
- **Code generation tooling** (`Benzene.CodeGen.*`) - .NET source generators and Go code
  generation work completely differently; if this port ever wants generated OpenAPI docs or a
  typed client, that's a fresh design, not a port of the C# generator.
- ~~**`Benzene.Mesh.*`** - doesn't need a per-language port~~ - **superseded.** This entry
  predates `docs/design/mesh.md`. The mesh as actually designed is not just an HTTP
  health-check aggregator: the service-side feeds (descriptor derivation from the live
  `Registry`, trace emission) are necessarily per-language, and this port ships them (`mesh`,
  `meshd` - see Done above). What stays true is that the *collector* is language-neutral: any
  implementation's collector can host any implementation's services over the shared
  `benzene:mesh:*` wire contract.

## Partial / deferred (implemented enough to interoperate, with a known boundary)

Honest record of where this port stops short of the .NET reference on purpose, so the boundary
is a decision rather than a surprise:

- **Inbound message versioning is not wired to the transports (tier C).** The data model is
  present - `Topic{ID, Version}`, versioned registration (`Register` keys on `(id, version)`),
  and the mesh descriptor carries a per-topic `version` - but no inbound binding reads the
  `benzene-version` header (or an HTTP `/v{version}` segment) off the wire, so a versioned
  handler is reachable only by explicit programmatic dispatch, not from an inbound message.
  This is deliberate: `benzene-version` is tier C in `wire-contracts.md` §2 (only meaningful
  for a service that opted into payload versioning), and the transport-metadata conformance
  fixture skips its version case for a non-versioning port accordingly. Before implementing the
  read path, one spec question needs resolving upstream: `core-concepts.md` §2 specifies
  handler selection as **exact version match only**, while `versioning.md` §3 and the .NET
  `VersionSelector` specify **exact match else highest available** - the two disagree, and a Go
  implementation should follow whichever the spec settles on rather than pick unilaterally. A
  read-path implementation must also stay non-regressive: a stray `benzene-version` on a message
  to a service that registered only unversioned handlers must still route to the unversioned
  handler, not fall to not-found.
- **Transparent payload up/down-casting (`versioning.md` §4, "Mechanism B") is not
  implemented.** It is explicitly opt-in in the spec (a topic without it "behaves exactly as an
  unversioned topic"), a conforming service needs neither versioning axis, and the .NET
  implementation leans on reflection-based property mapping this zero-dependency port avoids. If
  pursued it should be its own package, like the other dependency-bearing extensions.
- **Produced-vs-consumed version reconciliation.** Both halves of the `benzene:mesh:issues` feed
  now ship - the `meshd` collector (fingerprint merge + fleet view, conformance-verified against
  `mesh-issue-cases.json`) and the `mesh` emitter (`IssueMiddleware`/`PushIssueExporter`), see
  Done. The remaining mesh follow-up is the aggregator-level produced-vs-consumed version skew
  read model - an advanced mesh-UI feature, not a message-conformance requirement, and an additive
  follow-up rather than a gap in what ships. (The Go emitter leaves `exceptionType` empty: Go's
  router converts a handler panic to a `service-unavailable` result before the middleware sees it,
  so there is no language-native thrown type to capture - `exceptionType` is optional in §4.1 for
  exactly this reason, and classification falls to the status-based rows.)
