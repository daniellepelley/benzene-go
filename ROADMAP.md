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
- `awslambda` - AWS Lambda binding (hand-rolled Runtime API bootstrap, HTTP v2 + envelope
  adapters).
- `azurefunctions` - Azure Functions custom-handler binding.
- `conformance` - runs this port against the main repo's vendored language-neutral fixtures.
- Examples: `helloworld` (plain HTTP + DI + health check), `aws-lambda-helloworld`,
  `azure-functions-helloworld`, `gcp-cloudrun-helloworld` (no new package needed for GCP - see
  its README) - each with a matching CI build/test path and a gated GitHub Actions deploy
  workflow (`.github/workflows/deploy-*.yml`).

Every non-test-only package sits at 100% coverage or just under it with the gap being a
documented, genuinely-unreachable defensive branch - see each package's own comments.

## Next (zero new dependencies - in progress or planned)

These extend existing capabilities using only the standard library, matching this repo's
current zero-third-party-dependency posture:

1. **`client` package - outbound decorators.** transport-bindings.md §2: "Cross-cutting client
   behaviors (correlation ID injection, trace context, retry) are decorators over the same
   interface and therefore transport-agnostic." Introduces a `Sender` interface
   (`httpclient.Client` already satisfies it structurally - no changes needed there) plus
   `CorrelationDecorator` (writes `x-correlation-id` per wire-contracts.md §2 when the caller
   hasn't already set one) and `RetryDecorator` (retries a `ServiceUnavailable` result -
   wire-contracts.md §3's own "transient... retryable" status - with backoff).
2. **CORS middleware.** A portable, stdlib-only `Middleware` for HTTP-fronted services -
   origin/scheme/port matching, `Access-Control-Expose-Headers`, wildcard header support,
   preflight handling - mirroring the main repo's own portable CORS middleware design.
3. **`benzenetest` package - in-process test host.** A small fluent helper so an application's
   *own* tests can invoke a registered handler or a full pipeline directly (build a request,
   run it, assert on the `Result`) without spinning up real HTTP - mirroring `Benzene.Testing`/
   `BenzeneTestHost` in the main repo. This is about DX for *consumers* of this library, not
   this repo's own tests (which already use the pipeline/registry directly).

## Later - needs a dependency decision first

Per `CLAUDE.md`: no third-party dependency without asking first. These are real, valuable
extensions, but each needs an explicit yes on a specific dependency before starting, not a
unilateral add:

- **SQS / SNS / Kafka bindings.** Unlike Lambda's Runtime API (a small, stable, hand-rollable
  HTTP polling protocol) or Azure Functions' custom-handler contract (also a small hand-rolled
  JSON-over-HTTP contract), a real SQS/Kafka client means SigV4 request signing, connection
  pooling, consumer-group/offset management - reimplementing that from scratch is a correctness
  and security liability, not a reasonable zero-dependency stretch. Would need
  `github.com/aws/aws-sdk-go-v2` (SQS/SNS) or a Kafka client library (e.g.
  `github.com/segmentio/kafka-go` or `github.com/twmb/franz-go`).
- **gRPC binding.** Go has no gRPC support in the standard library at all; this needs
  `google.golang.org/grpc` + protobuf codegen tooling, a materially bigger dependency and
  build-step footprint than anything else in this repo.
- **EventBridge / DynamoDB Streams bindings.** Same shape as SQS - needs
  `aws-sdk-go-v2` for the outbound (`PutEvents`) side at minimum; the inbound (Lambda event)
  side could plausibly be hand-rolled similarly to `awslambda`'s existing HTTP v2 adapter, since
  it's "just" JSON event parsing, no signed API calls.
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
- **`Benzene.Mesh.*`** - the service-mesh visibility tooling is aggregator/UI infrastructure
  that talks to *any* language's health-check endpoint over HTTP; it doesn't need a per-language
  port at all, Go services already work with the existing (C#) aggregator once they expose the
  standard health-check response shape (which `healthcheck` already produces).
