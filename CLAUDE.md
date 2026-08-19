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
  cycle; everything else imports it. Each capability here carries both rungs of
  design-principles.md §4.1's ladder, and the shorthand is always literally composed from the
  explicit form - keep it that way when adding to it: `Register` returns the error /
  `MustRegister` panics with it (`mesh.RegisterOutbound`/`MustRegisterOutbound` mirror the pair);
  `UsePipeline` takes any pipeline / `UseDefaultPipeline` is one line of `UsePipeline(NewPipeline(
  RouterMiddleware(b.Registry)))`, which `App.Run` also installs when `Configure` set no pipeline,
  so the common case needs no `Configure` phase at all. That default exists as a **start-up**
  check as much as an ergonomic one: a builder with a nil `Pipeline` used to nil-deref on the
  first message inside a binding. `benzenetest.NewHost` re-implements the lifecycle to get its
  `WithServices` seam and must keep the same default - if you change one, change both.
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
- `httpbinding/` - the HTTP transport binding (native + envelope-over-HTTP entry points). `Route`
  matching supports a `"{name}"` placeholder per segment, optionally with a literal prefix/suffix
  in the same segment (e.g. `"v{version}"`); a `"{version}"` placeholder is additionally
  special-cased as versioning.md §2.1's HTTP-primary version carrier - it sets the dispatched
  topic's version directly (winning over a `benzene-version` header on the same request), and a
  route with no such segment falls back to the header. `awslambda.HTTPHandler` and
  `azurefunctions.Handler` share this `Route`/`RouteTable` matching, so the same behavior applies
  there too. `ListenAddr()` (+ `PortEnvVar`/`DefaultPort`) is the `$PORT` listen-address convention
  every container PaaS uses, owned here rather than hand-rolled in each `main()`;
  `azurefunctions.ListenAddr()` is the same shorthand for the Functions custom-handler's
  `FUNCTIONS_CUSTOMHANDLER_PORT`.
- `httpclient/` - the HTTP outbound client.
- `client/` - the outbound-client seam: `Sender` (the single interface every outbound transport -
  `httpclient`, `awssqs.Client`, the in-process sender, ... - satisfies) plus the `With*` decorators
  (`WithRetry`, `WithCorrelationID`, `WithTraceContext`) and `RegisterSender`/`GetSender` for wiring one
  onto a `Container`. An application constructs a `Sender` and uses it directly - there is no
  container-wide outbound routing table (a deliberate port divergence; see `inprocess`).
- `inprocess/` - an in-process `client.Sender` that dispatches an outbound send straight to a handler
  pipeline built in the same runtime (the shared `[]byte`/`json.RawMessage` envelope, no wire - not even
  loopback), for the modular-monolith case where a topic that used to leave the process no longer needs
  to. `PipelineSet` holds named pipelines (each its own `ApplicationBuilder`, so two may register the
  same topic with no collision); `NewSender`/`NewFanOutSender` target them by name. The divergence from
  .NET/TS (no outbound routing table, per-instance `Registry`) is spelled out in the package doc.
- `cors/` - a portable, stdlib-only CORS middleware for HTTP-fronted services (a Go port of
  `Benzene.Http/Cors`). CORS is an HTTP-transport concern (Origin, preflight OPTIONS, `Access-Control-*`
  headers), so unlike the pipeline middlewares this is an ordinary `net/http` middleware wrapping an
  `http.Handler` in front of `httpbinding.Handler`.
- `healthcheck/` - reserved-topic health-check interception middleware, plus ready-made `Check`
  implementations for probing a dependency's reachability: `TCPCheck` (opens a TCP connection -
  `Benzene.HealthChecks.Tcp`) and `HTTPPingCheck` (GETs a URL, healthy only on 200, credentials
  stripped from the reported URL - `Benzene.HealthChecks.Http`). Both zero-dep (net/net-http) and
  report a coarse error *category*, never the raw message (which can leak infra detail to an
  unauthenticated health caller). `DiskSpaceCheck` (`disk.go` - `Benzene.HealthChecks.Disk`) is the
  host self-check on free space: `WithMinimumFreeBytes`/`WithWarningFreeBytes` gate health on it (else
  it is pure telemetry), reporting freeBytes/totalBytes/usedPercent. Also zero-dep, but the one
  platform call (`diskUsage`) sits behind build tags - `disk_unix.go` (`syscall.Statfs`),
  `disk_windows.go` (`GetDiskFreeSpaceExW` via a lazy kernel32 binding, no `x/sys`), `disk_other.go`
  (an `unsupported-platform` fallback so it still compiles on js/wasm/plan9). The unix path runs in
  CI; the windows path is cross-compile-verified only (no live Windows), noted in its doc.
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
- `resilience/` - retry + timeout + bulkhead + fallback middleware, matching most of
  `Benzene.Resilience`(`.Polly`) (zero-dep; only the circuit breaker lives in its own
  `circuitbreaker` module for its library, and hedging is still to do). `Middleware(opts...)`
  re-invokes the downstream pipeline with exponential backoff. `Timeout(d)` bounds the downstream to
  a deadline via a **cooperative** `context.WithTimeout` (a handler that ignores its context can't be
  forcibly stopped in Go, so the wait is bounded only once such a handler returns; ctx-honoring
  handlers are bounded as expected), presenting the timed-out outcome as a `StatusTimeout` result -
  it calls `next` synchronously and never races on `ic.Result`, and a parent-ctx cancellation is not
  relabeled as a timeout. Because the Go router funnels application failures onto `ic.Result` (not a Go error),
  retry has two triggers mirroring .NET's `shouldRetry`/`shouldRetryContext`: `WithRetryOnError`
  (default: any error except context cancellation) for a `next()` error, and `WithRetryOnResult`
  (default: never - the lever services actually set: `RetryUnsuccessful` or
  `RetryOnStatus(...)`) for an unsuccessful `ic.Result`. Backoff is `sleep = jitter(min(maxDelay,
  initialDelay*factor^attempt))` with the cap/jitter on the sleep only (the growth curve stays
  uncapped - AWS "full jitter", `FullJitter` helper provided), a context-cancellable sleep, and an
  injectable `WithSleep` for tests. Re-invokes the whole downstream pipeline, so place it above
  idempotent outbound/port calls, never on an inbound step that already wrote a response.
  `Bulkhead(maxConcurrency, opts...)` caps concurrent invocations with a Polly-shaped two-permit
  semaphore (an execution pool + an admission pool of `maxConcurrency+WithMaxQueue`): past the cap it
  sheds load fast to a `too-many-requests` result (short-circuit-as-Result, like `ratelimiting`), and
  `WithMaxQueue(n)` lets up to n callers wait for a slot, each still context-bounded (a cancelled
  queued caller surfaces its cancellation and never takes a slot). `Fallback(fn, opts...)` substitutes
  a degraded `ic.Result` when an attempt is deemed a failure - a `next()` error (default: any except
  context cancellation) or an unsuccessful result (default `FallbackUnsuccessful`, narrow with
  `FallbackOnStatus(...)`) - the `fn` receives the cause and sets the substitute; place it above retry
  (fires after retries exhaust) or above a circuit breaker (degrades the open-state fail-fast to a
  cached response). Bulkhead and fallback share retry's `*Unsuccessful`/`*OnStatus` trigger vocabulary
  so the four compose predictably.
- `circuitbreaker/` - circuit-breaker middleware, in its **own Go module** (needs
  `github.com/sony/gobreaker/v2`), the library-backed slice of `Benzene.Resilience.Polly` that
  complements the zero-dep `resilience` (retry + timeout). `Middleware[T](cb, opts...)` runs the
  downstream inside `cb.Execute`: a genuine `next()` error counts as a breaker failure and propagates;
  a successful `next()` whose `ic.Result` is unsuccessful (per `WithTripOnResult`, default
  `TripUnsuccessful`; `TripOnStatus(...)` provided) counts as a breaker failure but returns nil with
  the result left on `ic.Result`; when the breaker is open/half-open-rejecting it **short-circuits
  without invoking the downstream** to a fail-fast status (`WithOpenStatus`, default
  `service-unavailable`; `WithOpenMessages`). Open-state is detected via a `called` flag captured in
  the `Execute` closure (robust against a downstream that itself returns gobreaker's sentinel errors),
  and the fail-fast result is built once at wiring time (a success-class open status panics at
  construction, never per-request). Its own module because gobreaker is third-party; the circuit
  breaker is the piece that genuinely wants a library (retry + a plain deadline do not - those stay
  zero-dep in `resilience`). Bulkhead/hedging/fallback remain deferred.
- `auth/` - authentication/authorization building block, matching `Benzene.Auth.Core`+`.Basic`+
  `.OAuth2` (zero-dep). Go has no `ClaimsPrincipal`, so a `Principal` (name/roles/claims) is a plain
  value threaded on the context (`ContextWithPrincipal`/`PrincipalFromContext`). `BasicAuth(validate,
  realm)` is the RFC 7617 authentication middleware (reads `Authorization: Basic`, validates via a
  `BasicValidator` the app supplies - no default, no hardcoded-credential footgun - and either sets
  the principal + calls next, or short-circuits `unauthorized` with a `WWW-Authenticate` challenge;
  splits on the first `:` so a password may contain one). `BearerAuth(validator, opts...)` (`bearer.go`) is the OAuth2/JWT bearer-token
  authentication middleware, the Go form of `Benzene.Auth.OAuth2`'s `OAuth2BearerMiddleware`: reads
  `Authorization: Bearer <jwt>`, validates via a `Validator` (`jwt.go`) and either sets the principal
  from the token's claims or short-circuits `unauthorized` with a **generic** message (never an
  oracle - the real reason only reaches the `WithOnError` hook, matching the .NET package's
  log-server-side-only stance). The security-critical JWT validation is pure standard library
  (`crypto/hmac`/`rsa`/`ecdsa` + `encoding/base64`/`json`), so the package stays zero-dependency where
  .NET leans on `Microsoft.IdentityModel`: `Validator` enforces an explicit **algorithm allowlist**
  (RFC 8725 §3.1 - `none` and any off-list `alg` rejected before a key is even resolved), verifies the
  signature for HS/RS/ES 256/384/512 with a strongly-typed `VerificationKey` per family (so an RS
  token can't be verified against an HMAC secret - the classic confusion), and checks iss/aud/exp/nbf/
  iat with clock skew. Keys come from `StaticKeys` (pinned HMAC/RSA/ECDSA keys, `jwtkeys.go`) or a
  `JWKSResolver` (`jwks.go` - fetches + caches a JWKS over HTTPS, refetches on an unknown `kid` for
  rotation, throttled; `NewJWKSFromAuthority` does OIDC `.well-known` discovery). `Authorize(predicate)`
  / `RequireRole(role)` / `RequireScope(scope)` are the authorization middleware (`forbidden` when the
  principal is present but not permitted, `unauthorized` when absent); `GrantedScopes` merges the
  `scope`/`scp` claim conventions. Header-based, so authentication is for HTTP-fronted pipelines.
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
- `clienthealthcheck/` - the consumer-side dependency health check, matching
  `Benzene.Clients.HealthChecks` (zero-dep). A `ServiceCheck` (a `healthcheck.Check`) probes a
  downstream Benzene provider through an outbound `client.Sender` and reports the **contract
  relationship**, not the provider's transient health: unreachable / serves no descriptor ->
  `failed`, reachable+matching contract hash -> `ok`, reachable+drifted hash -> `warning` (degraded,
  does **not** flip the caller's health), reachable without drift-detection configured -> `ok`
  (reachability only), reachable+hashless descriptor -> `ok` (drift unassessable, noted in `Data`).
  Both reachability **and** the hash come from the provider's reserved `benzene:mesh` descriptor,
  which `mesh.Middleware` serves with a success status *unconditionally* (health-independent) - so a
  reachable-but-unhealthy provider still reads as reachable. Deliberately **not** the
  `benzene:healthcheck` topic: that returns a failure status when the provider's own checks fail, and
  the envelope transport drops a failure body (`httpclient.toResult`), so a healthcheck probe could
  not tell "unhealthy" from "down" - and coupling this contract check to the provider's transient
  health is exactly what it must not do. .NET bakes the hash into a generated client; Go has none, so
  `WithExpectedContractHash` supplies the consumer's built-against hash (a documented divergence
  driven by the transport + the fact that Go's *descriptor*, not its health response, carries the
  hash). Register it on a **contracts** diagnostic surface, not a liveness/readiness probe (it calls
  a downstream). Its own package (not in `healthcheck`) so `healthcheck` keeps its net/net-http-only
  footprint - this check additionally needs `client` + `mesh`.
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
- `cloudservice/` - the one-call Cloud Service Profile *builder* (zero-dep), the assembly counterpart
  of `cloudserviceprobe` and the Go form of `Benzene.CloudService`. `New(name, registry, opts...)`
  wires the **synchronous HTTP surface** of the profile from a registry: R1 (hosted pipeline), R2
  (registry handlers via `RouterMiddleware`), R3 (`healthcheck.Middleware` + a `HealthPath` route),
  R4 (`EnvelopeHandler` at `EnvelopePath`, `/benzene/invoke`), R5 (`mesh.SpecHandler` at `SpecPath`),
  R7 (default `/benzene/*` paths), plus `mesh.Describe`+`mesh.Middleware` for the `benzene:mesh`
  descriptor - all over one `ApplicationBuilder`. Pipeline order is descriptor/health interception
  before `RouterMiddleware` so a reserved topic never falls through. Returns the `http.Handler`, the
  `Descriptor`, the `Builder`, and a **wiring** `ProfileReport` - a full **R1-R8** checklist
  (`Requirement{ID,Name,Satisfied,Detail}`, `Satisfied()`/`Unsatisfied()`). Crucially it is **honest**:
  `New` deliberately does not wire R6's outbound feeds (register/heartbeat/traces need a collector +
  push-exporter lifecycle the app owns) or R8 (trace propagation - `mesh.TraceMiddleware` inbound +
  the client `TraceContextDecorator` outbound), so `Satisfied()` is **false** for a `New`-only build
  and `Unsatisfied()` is the exact to-do list to reach full conformance (mirroring .NET's
  `CloudServiceProfileReport` evaluating all of R1-R8, not just the HTTP surface). `WithoutDescriptor()`
  additionally drops R5/R6 per §4 exposure control. It composes the existing pieces - a thin assembler;
  don't reimplement descriptor/health/spec logic here, and don't let the report over-claim conformance.
- `mesh/` - originally Phases 1-2 of this repo's own `docs/design/mesh.md`, now the main
  repo's `docs/specification/mesh.md` §§1-3: service `Descriptor` derived from the `Registry`
  (`topics` - what the service provides - + JSON Schemas derived at startup from the
  `TReq`/`TRes` types `Register` captures) AND from the `OutboundRegistry`
  (`consumes` - what the service consumes, mesh.md §2.3, `RegisterOutbound[TReq,TRes]` -
  mirrors `Registry`/`Register` exactly, minus the handler; a sender with no expected response
  type registers `TRes` as `any`, which schema derivation already maps to the unconstrained
  `{}` responseSchema), plus the contract `descriptorHash` (now sensitive to both lists),
  reserved-`benzene:mesh`-topic descriptor middleware, trace middleware + log exporter, and the
  issue feed's emitter half (`IssueMiddleware` + `PushIssueExporter`: source-side dedup by the
  normative §4.1 classification + SHA-256 fingerprint, delta counts, liveness flush;
  `ClassificationContractDrift` is exported for meshd's collector-derived §4.2 drift issues).
  Schema derivation is the one sanctioned use of `reflect` - startup-only, never on the
  dispatch path. Every feed is independent and optional - degradation (nil registry, nil
  outbound registry, nil or failing exporter, unprovisioned descriptor endpoint) must reduce
  the mesh, never break the service. The `benzene:mesh:*` wire topics and shapes (wire.go) are
  shared with the
  collector and promoted to the main repo's spec (`docs/specification/mesh.md` there, now
  the normative text; `docs/design/mesh-spec-draft.md` is the historical draft), pinned by
  the vendored `mesh-*.json` fixtures in `conformance/`. `SpecHandler(descriptor)` serves the same
  registry-derived descriptor as the Cloud Service Profile's R5 derived-spec document over a plain
  GET (mounted at `httpbinding.SpecPath`, `/benzene/spec`) - the profile permits Benzene's own
  derived format, and R5/R6 are then two surfaces onto the one registry-derived truth (GET spec vs
  the reserved `benzene:mesh` topic). Opt-in like the descriptor middleware - don't mount it and R5
  is simply reduced, per the profile's §4 exposure-control rule.
- `openapi/` - OpenAPI 3.0 document generation (zero-dep), the Go form of `Benzene.Schema.OpenApi`.
  `Generate(desc, opts...)` turns a `mesh.Descriptor` into an OpenAPI 3.0 document: each registered
  topic becomes a POST operation whose request body is the topic's request schema and whose
  responses carry the response schema (200) plus the framework failure vocabulary grouped by the
  HTTP codes `httpstatus.ToHTTP` maps them to. It **reuses mesh's derived schemas** (the one
  sanctioned reflection path) rather than deriving its own - the only reshaping is JSON Schema's
  nullable type-array (`["string","null"]`) into OpenAPI 3.0's `nullable: true`, handled for both
  the `[]string` (straight from `mesh.Describe`) and `[]any` (JSON-round-tripped) forms. `Handler`
  serves the doc over a plain GET, the OpenAPI sibling of `mesh.SpecHandler` (R5 is already satisfied
  by the derived descriptor; this is the richer industry-standard alternative). A documentation view
  of the message contracts, not a claim every topic is HTTP-routed.
- `asyncapi/` - AsyncAPI 3.0 document generation (zero-dep), the event-driven sibling of `openapi` and
  the other half of `Benzene.Schema.OpenApi`. `Generate(desc, opts...)` maps Benzene onto AsyncAPI
  3.0's channels + `action: receive`/`send` operations exactly as the .NET builder does: every
  **handled** topic is a `receive` operation on a channel carrying the request, with the native
  `reply` object pointing at a `<topic>:<suffix>` reply channel (default `response`,
  `WithResponseTopicSuffix`) - derived entirely from the descriptor. What a service **sends** (a
  fire-and-forget published event) is **not** in the descriptor, so it is a caller-declared input via
  `WithSentEvent(topic, payload)` (a `send` operation) - the same explicit input the .NET builder
  takes from broadcast-event/message-sender definitions, and the reason `openapi` deferred AsyncAPI
  rather than fabricating a sync-vs-event classification. Reuses mesh's derived schemas with **no**
  reshaping (AsyncAPI 3.0 schemas are JSON Schema Draft 7, so mesh's nullable `["T","null"]` form is
  already valid - unlike OpenAPI 3.0), deep-copied so `Generate` never mutates the descriptor.
  `Handler` serves it over a plain GET, the AsyncAPI sibling of `openapi.Handler`/`mesh.SpecHandler`.
- `meshd/` - the collector (main repo's `docs/specification/mesh.md` §4-§6, originally
  Phases 3-4 of this repo's own `docs/design/mesh.md`): an ordinary Benzene service
  (register/heartbeat/traces/issues ingest + `benzene:mesh:query:*` read models over an
  in-memory store with a bounded trace ring; the `benzene:mesh:issues` feed of mesh.md §4.1
  merges failure signatures by fingerprint and surfaces them on the fleet view) and the Mesh
  View (an embedded, self-contained HTML page - no JS framework, per the zero-dependency
  stance). Per the main repo's 2026-08 revision: the producer/consumer graph (providers AND
  consumers) is built from the latest registered ServiceDescriptor's `topics`/`consumes`
  alone (`store.register`) - it is fully declared, present for a service with zero traffic,
  and a redeploy replaces both edge sets wholesale. Trace parentage (`store.addEvents`) never
  touches that graph; it instead feeds invocation stats plus two additive, observed-only
  signals (mesh.md §4.2): per-declared-edge last-observed-at (`ProviderActivity`/
  `ConsumerActivity` on `TopicSummary` - "Unobserved", a decommission candidate, not a fact)
  and `contract-drift` issues for a *registered* service's traffic on a topic it didn't
  declare (`checkProviderDrift`/`checkConsumerDrift` - an anonymous/unregistered service is
  never flagged, it has no contract to diverge from). It must accept partial fleets: a
  missing feed renders a service as reduced (`missingFeeds`), never fails ingestion or
  queries. There is deliberately **no**
  Kubernetes API service-discovery counterpart to .NET's `Benzene.Mesh.Discovery.Kubernetes`
  (no `KubernetesServiceDiscoveryProvider` equivalent, no RBAC-scoped `list`/`get` on Services) -
  a documented divergence, not a gap on the punch list: this push-based collector already gives a
  fully live, real fleet (registered services + health + traces) with no pull side needed, and
  `examples/k8s-mesh-helloworld/` is the Kubernetes mesh estate built entirely on it (three
  domain services chaining over HTTP, reporting to `meshd`, no discovery RBAC at all) - see that
  example's README for the full story. The same divergence repeats, with the same reasoning, for
  AWS Lambda: there is no `AwsLambdaDiscoveryProvider`/`ListFunctions`+`ListTags` counterpart and
  no S3-backed mesh artifact/catalog store in this port, so `examples/aws-lambda-mesh/` (six
  chained Cloud Service Lambdas over SQS/SNS/EventBridge, a seventh mesh Lambda wrapping this same
  `meshd.Collector`) is push-based too - each service Lambda invokes the mesh Lambda directly via
  `awslambdaclient.Client` instead of the mesh discovering and interrogating them. Lambda adds one
  more wrinkle a long-lived K8s pod doesn't have: the collector's in-memory state is per execution
  environment, so the mesh Lambda pins `reserved_concurrent_executions = 1` in Terraform to keep
  that state a single consistent instance - see that example's README for the full story.
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
  `RELEASING.md` for the multi-module layout and why. Also carries `Consumer` - the **self-hosted
  SQS poller** (matching `Benzene.Aws.Sqs`'s `SqsConsumer`): a `Run(ctx)` loop that long-polls
  `ReceiveMessage`, dispatches each message through the pipeline in its own scope, and
  `DeleteMessageBatch`-deletes ONLY the ones whose dispatch succeeded - a failed message is left to
  reappear after its visibility timeout and go to SQS's own redrive/DLQ, never deleted unhandled. It
  is the standalone-compute alternative to the Lambda-trigger `Handler` (a container/EC2 worker that
  owns its poll loop), depends on a narrow `ReceiveDeleteAPI` for fake-based tests, and backs off on
  a transient AWS error rather than hot-looping.
- `awslambdaclient/` - **outbound Lambda-invoke client**, in its **own Go module** (needs
  `aws-sdk-go-v2/service/lambda`), the Go form of `Benzene.Clients.Aws.Lambda` and the invoking
  counterpart of the inbound `awslambda` binding. `Client` satisfies `client.Sender`: `Send` invokes
  a target function with a wire envelope payload. `RequestResponse` (default) parses the target's
  envelope response back into a `Result` (same `toResult` rules as `httpclient`); `Event`
  fire-and-forget returns `accepted` without a body; a set `FunctionError` (the target threw) becomes
  `unexpected-error`, not a mis-parsed success; a transport failure becomes `service-unavailable`.
  Narrow `InvokeAPI` for fake-based tests.
- `awsstepfunctions/` - **outbound Step Functions client**, in its **own Go module** (needs
  `aws-sdk-go-v2/service/sfn`), the Go form of `Benzene.Clients.Aws.StepFunctions`. `Client` satisfies
  `client.Sender`: `Send` starts a state-machine execution with the wire envelope as the `Input`.
  Starting is fire-and-forget, so a successful start is `accepted`; a transport failure is
  `service-unavailable`. An optional `ExecutionName` func derives an idempotent execution name
  (sanitized to Step Functions' rules, capped at 80 runes), and an `ExecutionAlreadyExists` error on
  a same-name retry is treated as an idempotent `accepted` (matching .NET's catch), not a failure.
- `azureservicebus/` - Azure Service Bus binding, in its **own Go module** (needs
  `azure-sdk-for-go/.../azservicebus`), the Go form of `Benzene.Clients.Azure.ServiceBus` (outbound)
  + `Benzene.Azure.ServiceBus` (the self-hosted worker). Outbound `Client` (satisfies `client.Sender`)
  sends one message per publish, topic written as the reserved topic **application property** last so
  it wins over a stray header, headers as the other string application properties, body verbatim; a
  successful send is `accepted`, a transport failure `service-unavailable`. Self-hosted `Worker` owns
  its own receive loop (`Run(ctx)` over a narrow `ReceiverAPI`) - the pull-loop counterpart of .NET's
  push `ServiceBusProcessor` and the sibling of `awssqs.Consumer`: dispatches each message in its own
  scope, `CompleteMessage`-completes only the ones whose dispatch succeeded, and settles a failed one
  per `AckMode` (`AckModeAbandon` default → redeliver; `AckModeDeadLetter` → quarantine). Settlement
  runs on a cancellation-detached context; `reservedNames` defaults to `builder.ReservedNames`.
- `azureeventhub/` - Azure Event Hubs binding, in its **own Go module** (needs
  `azure-sdk-for-go/.../azeventhubs/v2`), matching `Benzene.Azure.EventHub`. Outbound `Client`
  (satisfies `client.Sender`) publishes one event as a batch-of-one (`NewEventDataBatch` →
  `AddEventData` → `SendEventDataBatch`), topic + headers as the event's application properties. The
  `Consumer` reads over a narrow `Receiver` interface and hands checkpointing back to a **caller-owned
  `Checkpoint` hook** (Event Hubs checkpointing needs a blob-store checkpoint store the app owns - the
  worker deliberately does not implement it, a documented divergence). Topic/header/body resolution
  matches the Service Bus worker.
- `azureeventgrid/` - Azure Event Grid binding, in its **own Go module** (needs
  `azure-sdk-for-go/.../eventgrid/azeventgrid`), matching `Benzene.Clients.Azure.EventGrid`. Outbound
  CloudEvents `Client` (satisfies `client.Sender`): topic → CloudEvent `Type`, body → `Data` carried
  as `json.RawMessage` (so a JSON payload rides as JSON, never base64), headers → lowercased CloudEvent
  extension attributes. A successful publish is `accepted`, a transport failure `service-unavailable`.
- `azurequeuestorage/` - Azure Queue Storage binding, in its **own Go module** (needs
  `azure-sdk-for-go/.../storage/azqueue`), matching `Benzene.Clients.Azure.QueueStorage`. Outbound
  `Client` (satisfies `client.Sender`): `EnqueueMessage` with the **whole `wire.Request` envelope** as
  the message text (verbatim, not base64) - the same envelope-as-message-body convention the queue
  workers rehydrate. Successful enqueue → `accepted`, transport failure → `service-unavailable`.
- `azurecosmos/` - **self-hosted** Azure Cosmos DB Change Feed worker, in its **own Go module** (needs
  `azure-sdk-for-go/.../data/azcosmos`), matching `Benzene.Azure.CosmosDb` (the non-Functions flavor) -
  the standalone-compute counterpart of the zero-dep `azurefunctions.CosmosHandler` (where the
  Functions host owns the feed). A struct-fields + `Validate()` `Worker` (the unified self-hosted-worker
  convention) reads the change feed over a narrow `ChangeFeedReader` interface (`ReadNext(ctx,
  continuation, maxItems) (ChangeFeedPage, error)`; a fake tests it, no live Cosmos) and dispatches
  each page as **fan-in**, exactly like `CosmosHandler`: the whole batch of changed documents is ONE
  `envelope.DispatchTopicResult` invocation to the code-named `Topic` (body = JSON array, header
  `cosmos-document-count`), not one per document. Stop-at-batch-failure: an unsuccessful dispatch or a
  checkpoint error does NOT advance the continuation token, so the batch redelivers. Checkpointing is
  **caller-owned** (Cosmos needs an app-owned lease container) via a `Checkpoint(ctx, continuation)`
  hook run on a cancellation-detached context - the same divergence `azureeventhub.Consumer` documents.
  A `PollInterval` (no Event Hub equivalent) paces empty polls, since a caught-up change-feed read
  returns immediately rather than blocking. `NewChangeFeedReader` is the live-only SDK adapter
  (`adapter.go`, uncovered by design).
- `gcpfunctions/` - Google Cloud **Functions Gen2** inbound binding, in its **own Go module** (needs
  `GoogleCloudPlatform/functions-framework-go` + `cloudevents/sdk-go/v2`), the Go form of
  `GoogleCloud.Functions.Http` + `GoogleCloud.Functions.PubSub`. `RegisterHTTP(name, builder, routes)`
  registers a Gen2 HTTP function serving `httpbinding.Handler` (thin pass-through). `RegisterCloudEvent(
  name, builder, opts...)` registers a CloudEvent-triggered function (Pub/Sub/Eventarc): it maps the
  framework's `cloudevents.Event` onto a `wire.Request` by **reusing the root `cloudevents.ToRequest`**
  (type→topic, data→body, id/source/subject/extensions→`ce-`-prefixed headers) so the mapping is
  identical to this port's other CloudEvents surface, dispatches, and returns nil on a successful
  result / an error on an unsuccessful one - so a failure is retried by the platform, never silently
  dropped (the same outer-retry posture as `azurefunctions.EventGridHandler`). `WithReservedNames`
  (defaults to `builder.ReservedNames`; drives the no-`type` topic fallback to the reserved extension
  attribute) and `WithOnFailure`. The core `dispatchCloudEvent` is testable against a hand-built
  `event.Event`; the `functions.HTTP`/`functions.CloudEvent` registration is the thin live-only glue,
  pinned by compile-time signature assertions.
- `gcppubsubclient/` - Google Cloud Pub/Sub **outbound** client, in its **own Go module** (needs
  `cloud.google.com/go/pubsub`, which requires **go 1.25** - the one module forcing the workspace's
  go directive and CI `setup-go` to 1.25; the root and every other module still declare 1.24.7, so
  external consumers of those are unaffected). The invoking counterpart of the inbound `gcppubsub`
  push `http.Handler`. Interface-driven (`Publisher` narrow interface, `NewTopicPublisher` wraps a
  concrete `*pubsub.Topic` in `adapter.go`): `Send` publishes with topic + headers as Pub/Sub message
  **attributes** (empty headers dropped), body as `Data`; a successful publish is `accepted`, a
  transport failure `service-unavailable`.
- `rabbitmq/` - RabbitMQ binding, in its **own Go module** (needs `rabbitmq/amqp091-go` - an AMQP
  broker wire protocol isn't hand-rollable, same reason as `kafka`). Outbound `Client` (satisfies
  `client.Sender`) publishes with the topic as **both** the routing key and a `"topic"` header, the
  body as the AMQP body, `Persistent` delivery mode. Self-hosted `Consumer` (over a narrow
  `DeliverySource` interface) `Ack`s a successfully-dispatched delivery and `Nack`s a failed one,
  requeuing it exactly once (`!NoRequeue && !delivery.Redelivered`, so a poison message is bounded to
  one retry, then dropped/dead-lettered by the broker) - the AMQP sibling of `awssqs.Consumer` and the
  `kafka` worker.
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
- `logging/` - basic request logging/timing middleware using only `log/slog` (zero-dep): one
  structured line per pipeline invocation (topic/version, Benzene status, duration; Info/Warn/Error by
  outcome). The dependency-free visibility option - deliberately NOT the OTel-based `diagnostics` (no
  tracing/metrics/export). Register it outermost so it sees every invocation, including intercepted
  ones; it composes freely with `mesh.TraceMiddleware` and `diagnostics.Middleware`.
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
- `benzenetest/` - an in-process test host for a **consuming** application's own tests: boot the app's
  registered handlers + pipeline (middleware included) and drive a message through without a real
  HTTP/Lambda/Azure-Functions host. The Go counterpart of `Benzene.Testing`/`BenzeneTestHost`; the
  `go-test-champion` agent owns hardening it. (This repo's own tests build an `InvocationContext`
  directly where that's clearer, rather than routing through the harness.)
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
  credential secret being set, so it shows as skipped, not failed, until the repo owner
  configures deployment credentials. **The gate is a small `gate` job, not `if: secrets.X != ''`
  on the deploy job**: `secrets` is not one of the contexts GitHub exposes to a job-level `if:`
  (only `github`, `needs`, `vars`, `inputs` are), so that form makes the whole workflow fail to
  compile - it produced a *failed* run with zero jobs, which is the opposite of the intended
  skip, and it was silently red on main across all ten deploy workflows. The `gate` job reads the
  secret in a step `env:` (where `secrets` *is* available) and outputs a boolean the deploy job
  gates on via `needs.gate.outputs.has_credentials`. Repository **variables** may still be tested
  directly in the deploy job's `if` (`vars.MSK_CLUSTER_ARN != ''`), since `vars` is in that
  context. When adding a new cloud example, copy an existing workflow's `gate` job in the same
  commit, and document the required secrets/variables in that example's own README.

## Before making changes

- Read the relevant section of the main repo's `docs/specification/` first (it's usually
  cloned/available alongside this repo when doing cross-repo work) - don't invent behavior that
  the spec already defines.
- Read an existing package's pattern (doc comments, error handling, test style) before adding a
  new one - follow it rather than introducing a new convention.
- Tests are table-driven with `t.Run` subtests where cases share a shape, and named per-scenario
  test functions where distinct failure paths have distinct setup (both are idiomatic; pick per
  case). Read an existing package's tests and match its style rather than forcing one form.

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
- Run `go vet ./... ./awssqs/... ./awslambdaclient/... ./awsstepfunctions/... ./azureservicebus/...
  ./azureeventhub/... ./azureeventgrid/... ./azurequeuestorage/... ./azurecosmos/... ./circuitbreaker/...
  ./gcpfunctions/...
  ./gcppubsubclient/... ./rabbitmq/... ./awssns/... ./awseventbridge/... ./kafka/... ./diagnostics/...
  ./grpcbinding/... ./examples/aws-sqs-helloworld/... ./examples/aws-sns-helloworld/... && go build
  (same paths) &&
  go test (same paths) -race -cover` before considering a task
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
