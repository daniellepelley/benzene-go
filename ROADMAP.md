# benzene-go roadmap

What exists, what's next, and what's deliberately out of scope. This isn't a promise of
delivery order - just the current honest picture, kept up to date as things land.

## Done

- Core spec model: `Topic`, `Status`, `Result[T]`/`ResultInfo`, `Registry`, `Middleware`/
  `Pipeline`, `RouterMiddleware`, the DI-lite `Container`/`Scope` (+ `ScopeFromContext` for
  handler-level resolution), the three-phase `App` lifecycle.
- `wire` - the transport-neutral envelope.
- `httpstatus` - the Benzene<->HTTP status mapping tables (conformance-verified).
- `envelope` - `wire.Request` -> `Pipeline` -> `wire.Response` dispatch, shared by every
  HTTP-shaped binding below.
- `httpbinding` - native REST-style HTTP binding + envelope-over-HTTP.
- `httpclient` - the HTTP outbound client (one `Send` method).
- `healthcheck` - reserved-topic health-check interception middleware.
- `logging` - basic request logging/timing middleware using only `log/slog`: one structured
  line per invocation (topic/version, Benzene status, duration; Info/Warn/Error by outcome).
  The dependency-free visibility option alongside the `diagnostics` module's full OTel feed.
- `awslambda` - AWS Lambda binding (hand-rolled Runtime API bootstrap, HTTP + envelope
  adapters; the HTTP adapter handles Function URL / API Gateway v2.0, API Gateway
  REST/v1.0, and ALB target-group event shapes, detected per invocation).
- `azurefunctions` - Azure Functions custom-handler binding: `Handler` for HTTP-triggered
  functions, `QueueHandler` for queue-shaped triggers (Storage Queue, Service Bus) with
  wire-contracts §2 topic resolution and platform-native retry on failure.
- `client` - outbound-client decorators (`CorrelationDecorator`, `RetryDecorator`) over a
  transport-agnostic `Sender` interface; `httpclient.Client` satisfies it structurally.
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
  `Registry` (topics + startup-derived JSON Schemas + `descriptorHash`), reserved-`mesh`-topic
  descriptor middleware, `TraceMiddleware` with W3C `traceparent` propagation, and the
  `LogExporter`/`PushExporter` trace feeds - every feed independent and optional.
- `meshd` - Phases 3-4 of `docs/design/mesh.md`: the collector (register/heartbeat/traces
  ingest + `mesh:query:*` read models over an in-memory store with a bounded trace ring) and
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
- `awseventbridge` - AWS EventBridge binding, in its **own Go module** (see `RELEASING.md`):
  an inbound `Handler` for a Lambda invoked by an EventBridge rule (zero dependencies;
  `detail-type` carries the topic so rules pattern-match per Benzene topic, an
  envelope-shaped `detail` is unwrapped so wire headers travel, and a failed event returns a
  Go error triggering AWS's async-invoke retry - the same posture as `awssns`), and an
  outbound `Client` publishing via `PutEvents` (needs `aws-sdk-go-v2/service/eventbridge`).
- `diagnostics` - OpenTelemetry-based diagnostics middleware, in its **own Go module** (see
  `RELEASING.md`) - the Go equivalent of the main repo's `Benzene.Diagnostics`: one server
  span per invocation (topic-named, W3C traceparent join, `benzene.topic`/`benzene.status`
  attributes) plus invocation count/duration metrics. Depends on the OpenTelemetry *API*
  only (`go.opentelemetry.io/otel`); the application owns the SDK and exporter, and standard
  OTLP export covers Datadog/Zipkin/etc. without vendor-specific packages (as promised
  below).
- `kafka` - Kafka binding, in its **own Go module** (see `RELEASING.md`): a `Consumer` loop
  over a consumer group (one pipeline invocation + DI scope per record, explicit commits;
  Kafka has no broker-side redelivery/DLQ, so a failed message goes to the `OnFailure` hook -
  dead-letter publish, log - and is then committed past, keeping the partition moving) and an
  outbound `Client` satisfying `client.Sender`. Needs `github.com/segmentio/kafka-go` (chosen
  over `franz-go` for its narrow Reader/Writer surface, which this repo's
  fake-behind-an-interface test style wraps cleanly) - a broker wire protocol is not
  reasonably hand-rollable, hence the module split.
- `conformance` - runs this port against the main repo's vendored language-neutral fixtures.
- Examples: `helloworld` (plain HTTP + DI + health check), `aws-lambda-helloworld`,
  `azure-functions-helloworld`, `gcp-cloudrun-helloworld` (no new package needed for GCP - see
  its README), `aws-sqs-helloworld` (publisher + consumer Lambdas, its own module),
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

- **gRPC binding.** Go has no gRPC support in the standard library at all; this needs
  `google.golang.org/grpc` + protobuf codegen tooling, a materially bigger dependency and
  build-step footprint than anything else in this repo.
- **DynamoDB Streams binding.** EventBridge is now done (`awseventbridge`, see Done above);
  a Streams-triggered Lambda is the same shape as `awssqs`'s inbound handler (a Records
  batch with `batchItemFailures` support) - the inbound side is hand-rollable, and there is
  no outbound side to need an SDK, so this could even be zero-dependency in the root module.
- **Pub/Sub outbound (publish) client.** The inbound half is done with zero dependencies
  (`gcppubsub` - a push subscription is just HTTPS in). Publishing needs OAuth-signed API
  calls, i.e. `cloud.google.com/go/pubsub` - the same shape as `awssqs`/`awssns`'s outbound
  clients, and like them it would live in its own module so the dependency doesn't spread.
- **Google Cloud Functions Gen2 (buildpack) deploy**, as opposed to the Cloud Run path already
  documented in `examples/gcp-cloudrun-helloworld` - needs
  `github.com/GoogleCloudPlatform/functions-framework-go`, the one Google-specific dependency
  this port has avoided by targeting Cloud Run instead.

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
  `mesh:*` wire contract.
