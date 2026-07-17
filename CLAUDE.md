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

## Structure

- Root package (`benzene`) - Topic, Status, Result[T], Registry, Middleware/Pipeline, the
  DI-lite Container/Scope, the three-phase App lifecycle. No sub-package may import this in a
  cycle; everything else imports it.
- `wire/` - the transport-neutral message envelope. Deliberately has **no dependency on the
  rest of this module** - keep it that way (see the package doc comment).
- `httpstatus/` - the Benzene<->HTTP status mapping tables, cross-checked against
  `docs/specification/conformance/http-status-mapping.json` in the main repo.
- `envelope/` - dispatches a `wire.Request` through a `Pipeline`, shared by `httpbinding`,
  `httpclient`, and `conformance`.
- `httpbinding/` - the HTTP transport binding (native + envelope-over-HTTP entry points).
- `httpclient/` - the HTTP outbound client.
- `healthcheck/` - reserved-topic health-check interception middleware.
- `mesh/` - Phases 1-2 of `docs/design/mesh.md`: service `Descriptor` derived from the
  `Registry` (topics + JSON Schemas derived at startup from the `TReq`/`TRes` types the
  Registry captures at `Register` time, plus the contract `descriptorHash`),
  reserved-`mesh`-topic descriptor middleware, and trace middleware + log exporter.
  Schema derivation is the one sanctioned use of `reflect` - startup-only, never on the
  dispatch path. Every feed is independent and optional - degradation (nil registry, nil
  or failing exporter, unprovisioned descriptor endpoint) must reduce the mesh, never
  break the service. The `mesh:*` wire topics and shapes (wire.go) are shared with the
  collector and promoted to the main repo's spec (`docs/specification/mesh.md` there, now
  the normative text; `docs/design/mesh-spec-draft.md` is the historical draft), pinned by
  the vendored `mesh-*.json` fixtures in `conformance/`.
- `meshd/` - Phases 3-4 of `docs/design/mesh.md`: the collector - an ordinary Benzene
  service (register/heartbeat/traces ingest + `mesh:query:*` read models over an
  in-memory store with a bounded trace ring) and the Mesh View (an embedded,
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
  redelivery/poison-queue machinery).
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
  `segmentio/kafka-go` - a broker wire protocol isn't hand-rollable): `Consumer` loop (one
  scope per record, explicit commits; no broker-side redelivery/DLQ exists, so failures go to
  the `OnFailure` hook and are committed past) + outbound `Client` satisfying `client.Sender`.
  Both halves depend on narrow interfaces (`MessageSource`, `MessageWriter`) so tests run
  against fakes, no live broker.
- `conformance/` - the fixture runner; `testdata/*.json` are vendored copies from the main
  repo's `docs/specification/conformance/` (see `conformance/README.md` for how to re-sync).
- `examples/` - runnable example services: `helloworld` (plain HTTP),
  `mesh-helloworld` (collector + two meshed services, the Phases 1-4 demo), and one
  `<provider>-helloworld` per cloud deployment target (`aws-lambda-helloworld`,
  `azure-functions-helloworld`, `gcp-cloudrun-helloworld`, `aws-sqs-helloworld`,
  `aws-sns-helloworld`, `gcp-pubsub-helloworld`) - each with its own README stating the concrete
  deploy steps and exactly what was/wasn't verified without live cloud credentials. Plain Cloud
  Run needs no dedicated package (see `gcp-cloudrun-helloworld/README.md`); `gcppubsub` exists
  because the Pub/Sub push envelope is a concrete shape `httpbinding` alone can't cover - keep
  applying that bar to any new platform package. `aws-sqs-helloworld` and
  `aws-sns-helloworld` are each their own module (depends on both the root module and its
  respective binding - would be a cycle inside either).
- `go.work` - ties the root module, `awssqs/`, `awssns/`, `awseventbridge/`, `kafka/`,
  `diagnostics/`, `examples/aws-sqs-helloworld/`, and `examples/aws-sns-helloworld/` together
  for local development (see `RELEASING.md`). Its `replace` lines are workspace-scoped only
  and never affect real external consumers.
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
  .NET original. `awssqs`, `awssns`, `awseventbridge`, `kafka`, and `diagnostics` are the
  deliberate exceptions (needing `aws-sdk-go-v2` service clients for signed API calls,
  `segmentio/kafka-go` for the broker wire protocol, and `go.opentelemetry.io/otel` for the
  OTel API) and each lives in its own module specifically so that exception doesn't spread.
  Ask before
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
  ./diagnostics/... ./examples/aws-sqs-helloworld/... ./examples/aws-sns-helloworld/... &&
  go build (same paths) && go test (same paths) -race -cover` before considering a task
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
