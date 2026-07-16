# Benzene Mesh — design

**Status: ALL PHASES COMPLETE (this repo's side).** The in-process `mesh/` package
(descriptor, reserved-topic middleware, trace middleware, log + push exporters, schema
derivation + `descriptorHash`, span propagation), the `meshd/` collector with the Mesh
View, and the `examples/mesh-helloworld` end-to-end demo are implemented in this repo.
Phase 5's spec promotion is **merged to the main repo's `main`**: the mesh wire
contracts are now normative as `docs/specification/mesh.md` there, with three
`mesh-*-cases.json` conformance fixtures that are vendored into this repo's
`conformance/` and passing - this port is that spec's reference implementation. C# parity
(reconciling the main repo's pre-existing `Benzene.Mesh.*` visibility packages onto the
promoted contracts) and the cross-language fleet demo are scoped as .NET-side work in
the main repo's `work/service-mesh-roadmap-1.0.md`.
[`mesh-spec-draft.md`](./mesh-spec-draft.md) is kept as the historical draft the
promotion was authored from.

A degradation rule became explicit during Phase 1 implementation and binds every later
phase: **every mesh feed is independent and optional, and an unavailable feed reduces
the mesh, never the service - and never the other feeds.** A deployment whose descriptor
endpoint is deliberately not provisioned (e.g. withheld pending a security review) still
runs the trace feed; a nil registry yields a descriptor without topics (recorded in the
descriptor's `degraded` list rather than passed off as "no topics"); a nil, failing, or
panicking exporter never affects the invocation it observed. `meshd` (Phase 3) must
accept partial fleets the same way: traces without a matching descriptor render as an
anonymous-but-live service, descriptors without traces render as a catalog entry with
no stats.

---

## 1. Thesis: the catalog is derived, not declared

Every serious platform team ends up hand-building the same thing: a live map of
*what services exist, what they accept, whether they're healthy, and what's actually
flowing between them*. Today that map is assembled from parts that don't know about
each other — a hand-maintained service catalog, a per-cloud monitoring stack, a
tracing pipeline, an API spec repo — and it goes stale the moment an engineer ships
without updating the YAML.

Benzene services are different in one structural way: **the information the map needs
already exists, machine-readable, inside every running service** —

| The map needs | A Benzene service already has |
|---|---|
| What operations does this service expose? | The `Registry`: every `Topic` (+ version) is explicitly registered (core-concepts.md §9 — explicit registration is the *only* discovery mechanism) |
| What are the request/response shapes? | `Handler[TReq, TRes]` — the concrete Go types are known at the `Register` call site; a JSON Schema can be derived from them at startup |
| Is it healthy, and why/why not? | The reserved `healthcheck` topic and the standard aggregate response (wire-contracts.md §5) — already implemented in `healthcheck/` |
| What is actually flowing, succeeding, failing? | Every invocation passes through the `Pipeline` with a `Topic` and ends in a `Status` from a shared vocabulary — one middleware sees everything |
| Does this work the same on AWS, Azure, GCP? | The wire envelope is transport- and vendor-neutral by construction; the same service runs on Lambda, Functions, Cloud Run, or plain HTTP today (`examples/`) |

So Benzene Mesh is **not** a sidecar mesh and **not** a metadata catalog you fill in.
It is: a small in-process package that lets every service *describe itself* and
*report what it's doing*, a collector that is itself an ordinary Benzene service, and
one view — the **Mesh View** — that shows the whole fleet, across clouds, with data
that cannot go stale because it is emitted by the running code itself.

That view is the product. If it's good enough, it becomes the reason to choose
Benzene: adopt the library and the fleet map, health matrix, live traffic stats,
schema catalog, and cross-cloud traces come with it — with zero sidecars, zero agents,
zero YAML, and zero third-party dependencies.

## 2. The problems this solves (researched)

What developers and platform teams actually report struggling with — condensed here;
the full findings, the why-incumbents-can't-fix-it analysis per problem, and the
positioning material for future writing live in [`mesh-research.md`](./mesh-research.md):

**No unified view across clouds.** In a 2025/26 survey of IT leaders running
hybrid/multi-cloud, the single top pain point (47%) was *"getting a global view of
utilization and spend"*; 78% report being overwhelmed by the number of cloud
management tools, and without a unified view resource sprawl goes unnoticed
([nOps](https://www.nops.io/blog/what-are-the-challenges-to-multi-cloud-management/),
[Synergy Labs](https://www.synergylabs.co/blog/multi-cloud-strategy-in-2026-avoid-vendor-lock-in-without-doubling-your-complexity)).
Each provider brings its own identity model, audit logs, and dashboards, so "the
whole estate" exists only in people's heads
([flolive](https://flolive.net/blog/glossary/multi-cloud-in-2026-architecture-challenges-and-best-practices/)).

**Tracing pipelines are hard to run and harder to use.** An industrial survey of
microservice tracing across ten companies found that even where distributed tracing
is deployed, teams struggle to turn the flood of spans into answers — prioritizing
alerts and drawing conclusions from trace data is the recurring pain, not span
collection itself
([Empirical Software Engineering / ACM](https://dl.acm.org/doi/10.1007/s10664-021-10063-9),
[IEEE Access](https://ieeexplore.ieee.org/iel8/6287639/10820123/10967524.pdf)).
Generic traces are HTTP-shaped: a span says `POST /api/v2/orders → 500`, not
*"topic `order:create@v2` returned `ValidationError`"*.

**Service catalogs go stale, then trust collapses.** Backstage-style catalogs are
driven by hand-maintained YAML; the dependency graph is "whatever a human last wrote
there," and once the data is visibly wrong, adoption collapses — one analysis puts
full trust in documentation repositories at 3% of engineers, and typical catalog
stand-up cost at 2–3 engineers for 6+ months
([Riftmap](https://riftmap.dev/blog/backstage-alternatives/),
[Medium — Backstage Backlash](https://medium.com/@samadhi-anuththara/backstage-backlash-why-developer-portals-struggle-cb82d4f082e1),
[Roadie](https://roadie.io/blog/3-strategies-for-a-complete-software-catalog/)).

**Service meshes are a complexity tax — and don't reach serverless.** Istio's own
ecosystem concedes the complexity criticism: sidecars in every pod, iptables
interception, a second distributed system to operate
([Solo.io](https://www.solo.io/blog/service-mesh-should-not-be-complex),
[earezki](https://earezki.com/you-dont-need-service-mesh/)). And a sidecar mesh
structurally cannot cover AWS Lambda, Azure Functions, or (fully) Cloud Run — exactly
the targets this repo ships bindings for. An application-level mesh goes where
sidecars can't.

**Schema drift breaks consumers silently.** A renamed or retyped field ships without
a version bump and a downstream service mis-parses for weeks before anyone notices;
contract testing exists precisely because there is usually no live record of what
producers actually emit vs. what consumers actually expect
([Total Shift Left](https://totalshiftleft.ai/blog/api-schema-validation-catching-drift),
[Medium — Contract Drift](https://medium.com/@gunashekarr11/contract-drift-schema-mismatch-detection-the-most-underrated-api-failure-in-modern-systems-c278a2914205)).

Mapping problems to mesh features:

| Reported pain | Mesh answer |
|---|---|
| No global view across clouds | One Fleet Overview: every service, every cloud, one screen (§6.1) |
| Traces are floods of HTTP spans | Traces are *semantic*: topic + version + Benzene status, uniform across clouds (§5.2) |
| Catalog YAML goes stale | Descriptor derived from the live `Registry` at startup; "last seen" is a heartbeat, not a wiki edit (§5.1) |
| Sidecar mesh complexity; no serverless coverage | In-process middleware, zero sidecars, works on Lambda/Functions/Cloud Run because it's just the pipeline (§4) |
| Silent schema drift | Producers publish derived schemas; the collector diffs versions and flags consumers still sending the old shape (§6.2) |

## 3. What Benzene Mesh is (and is not)

**Is:** an *application-level* mesh — service self-description, health, semantic
traces, live stats, and one view over all of it, built from Benzene's existing
primitives (reserved-topic interception, middleware, the wire envelope).

**Is not:**

- **Not a traffic-managing service mesh.** No sidecars, no mTLS termination, no
  retries/routing/load-balancing. Traffic still flows exactly as it does today.
- **Not an OpenTelemetry replacement.** Mesh trace events carry and propagate W3C
  `traceparent` (already a spec header — see the wire tests), so mesh traces
  correlate with any existing OTel pipeline. The mesh adds the Benzene-semantic
  layer OTel can't know about; it doesn't compete on infrastructure spans.
- **Not a new dependency.** Everything below is standard library, in keeping with
  this repo's zero-dependency rule.

## 4. Architecture

Three parts. The collector and view are themselves Benzene services — the mesh is
built *on* Benzene, which is both dogfooding and the proof of the multi-cloud claim.

```
┌────────────────────────── each Benzene service ──────────────────────────┐
│  Pipeline:  [mesh.Middleware] → [healthcheck] → [router] → handler       │
│               │        │                                                 │
│               │        └─ intercepts reserved "mesh" topic:              │
│               │           replies with the ServiceDescriptor (§5.1)      │
│               └─ observes every invocation: emits TraceEvents (§5.2)     │
│                  to an Exporter (buffered, batched, non-blocking)        │
└──────────────────────────────────────┬────────────────────────────────────┘
                                       │ wire envelope (mesh:* topics)
                                       ▼
┌ meshd — the collector, an ordinary Benzene service ───────────────────────┐
│ topics: mesh:register  mesh:heartbeat  mesh:traces  mesh:query:*         │
│ store: pluggable (MVP: in-memory + periodic snapshot)                     │
│ derives: fleet state, topic catalog, dependency graph, stats, drift      │
└──────────────────────────────────────┬────────────────────────────────────┘
                                       │ mesh:query:* topics
                                       ▼
                         Mesh View (the dashboard, §6)
```

### 4.1 The `mesh` package (in-process)

Follows the `healthcheck` package's pattern exactly — a reserved-topic interception
middleware plus an observation wrapper:

- `mesh.Describe(reg *benzene.Registry, info ServiceInfo) Descriptor` — builds the
  service descriptor from the live Registry (topics + versions) plus static identity
  (service name, version, instance id) and detected placement (§4.3). Request/response
  JSON Schemas are derived once, at startup, from the registered `TReq`/`TRes` types
  via stdlib `reflect` — startup-only, never on the hot path, and consistent with the
  existing rule that *dispatch* avoids reflection (`ResultInfo` stays as-is).
- `mesh.Middleware(desc Descriptor)` — intercepts the reserved `mesh` topic
  (aliasable, like `healthcheck`) and short-circuits with the descriptor. Any other
  topic passes through unchanged.
- `mesh.TraceMiddleware(exporter Exporter)` — wraps the pipeline; for every
  invocation records `{topic, version, status, duration, traceparent, correlation
  id}` and hands a `TraceEvent` to the exporter. Panics and missing handlers are
  *already* converted to Results downstream, so the middleware sees a status for
  every invocation — the "never return a Go error to the transport" rule is what
  makes 100% trace coverage structural rather than aspirational.
- `Exporter` is an interface. Ships with: `LogExporter` (JSON lines to a writer),
  `PushExporter` (batches to `meshd` via `httpclient` — mesh traffic is itself wire
  envelopes), and a no-op. Export is buffered and lossy-by-design under backpressure:
  **the mesh must never make a service slower or less reliable than an unmeshed one.**

### 4.2 `meshd` — the collector / control plane

An ordinary Benzene service (deployable to any of the three clouds via the existing
bindings — the `examples/` pattern applies). Registered topics:

- `mesh:register` — a service announces its descriptor at startup.
- `mesh:heartbeat` — periodic; carries the aggregate health response (§5 shape,
  reused verbatim) plus the descriptor hash, so a redeploy with new topics is
  detected as a hash change and triggers re-registration.
- `mesh:traces` — batched TraceEvents.
- `mesh:query:fleet`, `mesh:query:service`, `mesh:query:topic`, `mesh:query:trace` —
  read models for the view.

From traces alone, `meshd` derives what no one has to declare: the **dependency
graph** (span parentage across services), **per-topic live stats** (rate, latency
percentiles, status mix), **consumer lists** (who calls topic X, at which version),
and **drift signals** (§6.2). Pull-based fallback: for environments where push is
awkward, `meshd` can poll each service's reserved `mesh` + `healthcheck` topics —
both are just envelopes over HTTP.

### 4.3 Placement detection

Cloud/region/runtime are read from each platform's documented environment (no new
config to maintain, honest about its limits): `AWS_LAMBDA_FUNCTION_NAME`/`AWS_REGION`
on Lambda, `FUNCTIONS_WORKER_RUNTIME`/`WEBSITE_SITE_NAME` on Azure Functions,
`K_SERVICE`/`K_REVISION` on Cloud Run, else "self-hosted" with an explicit override.
Per this repo's rules, each detection ships with tests against the platform's
documented contract and a README note about what was and wasn't verifiable without
live credentials.

## 5. Wire contracts (proposed for the main-repo spec)

All shapes follow the existing envelope conventions: camelCase field names, flat
string→string headers, pre-serialized string bodies, and the shared status
vocabulary. Language-neutral by design — a C# service and a Go service must produce
byte-compatible descriptors and trace events, and conformance fixtures
(`mesh-cases.json`) would be authored alongside the spec change.

### 5.1 ServiceDescriptor

```json
{
  "service": "orders",
  "serviceVersion": "1.4.2",
  "instanceId": "orders-7f9c…",
  "runtime": "go",
  "binding": "aws-lambda",
  "placement": { "cloud": "aws", "region": "eu-west-1" },
  "topics": [
    {
      "id": "order:create",
      "version": "v2",
      "requestSchema":  { "…derived JSON Schema…": true },
      "responseSchema": { "…derived JSON Schema…": true }
    }
  ],
  "descriptorHash": "sha256:…",
  "degraded": ["registry"]
}
```

`degraded` (usually absent) lists the feeds that were unavailable when the descriptor
was built - e.g. `"registry"` when the topic-catalog feed wasn't wired up - so a reduced
mesh is visible as reduced rather than mistaken for a service with no topics. Schemas
and `descriptorHash` are Phase 2; the Phase 1 descriptor ships the shape above without
them.

### 5.2 TraceEvent

```json
{
  "traceId": "4bf92f35…",
  "spanId": "00f067aa…",
  "parentSpanId": "0af7651a…",
  "service": "orders",
  "instanceId": "orders-7f9c…",
  "topic": "order:create",
  "topicVersion": "v2",
  "status": "ValidationError",
  "durationMs": 12.4,
  "startedAt": "2026-07-16T09:14:03.120Z",
  "correlationId": "abc-123"
}
```

`traceId`/`spanId`/`parentSpanId` are the W3C `traceparent` fields — propagated on
the existing header, so mesh traces interleave cleanly with OTel spans from
non-Benzene infrastructure. `status` is the Benzene status verbatim: this is what
makes a mesh trace *semantic* — the view can say "`order:create@v2` is returning
`ValidationError` for 4% of calls from `checkout`" instead of "some POST returned 400."

### 5.3 Heartbeat

The §5 health `Response` (reused byte-for-byte — `isHealthy` + `healthChecks`),
wrapped with `service`, `instanceId`, `descriptorHash`, and `sentAt`. No new health
vocabulary; the mesh consumes exactly what `healthcheck/` already produces.

## 6. The Mesh View

One screen answers "what is going on, everywhere, right now?"; everything else is a
drill-down. A static mockup of the fleet screen lives at
[`mesh-view-mockup.html`](./mesh-view-mockup.html) in this directory.

### 6.1 Fleet Overview (the home screen)

- **Fleet stat row**: services live, topics served, invocations/min, fleet error
  rate, unhealthy count — the five numbers a platform owner checks first.
- **Services grouped by cloud** (AWS / Azure / GCP / self-hosted columns): each
  service is a card with health (icon + label, never color alone), binding, region,
  a rate sparkline, p95 latency, and error %. A degraded health check or a spiking
  error rate surfaces here without navigation. This is the multi-cloud "single pane"
  that survey respondents said doesn't exist — and it's real, because every card is
  a live heartbeat, not a catalog entry.
- **Staleness is explicit**: a service that missed heartbeats shows *last seen 4m
  ago* — the anti-stale-catalog property made visible.

### 6.2 Topic Catalog

The fleet's API surface as a table, derived entirely from descriptors + traces:
topic@version, providing service(s), consuming services *observed in traffic* (from
trace parentage — not what a wiki claims), live rate, status mix, and schema.

Drift signals, computed by `meshd`:

- **Version skew** — traffic still arriving at `order:create@v1` after `v2`
  registered: listed with *which consumers* are still on v1 (the migration to-do
  list generates itself).
- **Schema change without version bump** — descriptor hash changed for an existing
  topic version between deploys: flagged as a probable breaking change, with the
  schema diff.
- **Orphaned topics** — registered but no traffic in N days; **unknown topics** —
  traffic addressed to a topic no live service registers (the `NotFound`s tell you).

### 6.3 Service Detail

Per-service: descriptor (topics, schemas, binding, placement), the health-check
breakdown over time (which *named check* failed, when — the §5 `healthChecks` map is
already per-check), per-topic stats, and the dependency neighborhood in/out derived
from traces.

### 6.4 Flow Explorer

A trace waterfall by `traceId`/correlation id — but cross-cloud and semantic: each
row is *service + topic@version + Benzene status + duration*, so "checkout (AWS) →
orders (GCP) → payments (Azure)" reads as one flow. Filterable by status class
("show me every flow that ended `ServiceUnavailable` in the last hour"), which is
the alert-prioritization answer the tracing survey found teams missing.

### 6.5 Health Matrix

Services × time: heartbeat health as a strip per service, aligned across the fleet —
"what went unhealthy together at 09:14?" answered visually. Per-check drill-down via
§6.3.

## 7. Why this makes Benzene the obvious multi-cloud choice

The pitch, in the order a platform evaluator will hear it:

1. **The map is free and cannot lie.** Register your handlers (which you do anyway)
   and the catalog, schemas, health, and dependency graph exist — derived from
   running code, current by construction. Competing catalogs cost multiple
   engineer-years and decay.
2. **One observability semantics across clouds.** The same five statuses, the same
   envelope, the same trace shape on Lambda, Functions, Cloud Run, and bare HTTP.
   Nothing else offers cross-cloud *semantic* uniformity, because nothing else owns
   the invocation model on all of them.
3. **Mesh benefits without mesh operations.** No sidecars, no CRDs, no second
   distributed system — and it reaches serverless, where sidecar meshes structurally
   can't go.
4. **Drift becomes visible before it becomes an outage.** Producers publish what
   they actually serve; the collector watches what consumers actually send.
5. **Leave anytime.** Zero dependencies, W3C-compatible tracing, spec'd wire shapes
   with conformance fixtures. The anti-lock-in posture is itself the differentiator
   for multi-cloud buyers.

## 8. Delivery phases

Each phase is a shippable unit per this repo's workflow rules (package + tests +
docs in one commit, 100%-or-documented coverage):

1. **`mesh` package** - ✅ implemented: `Descriptor` from Registry (via the new
   `Registry.Topics()`), reserved-topic middleware, `TraceMiddleware` + `LogExporter`,
   placement detection. Immediately useful standalone (structured invocation logs with
   zero setup).
2. **Schema derivation** (`reflect`-based, startup-only) + `descriptorHash` - ✅
   implemented: per-topic request/response schemas derived from the `TReq`/`TRes` types
   the Registry now captures at the `Register` call site (`Registry.TopicTypes`), and the
   contract hash (excluding per-instance and transient fields, so two instances of one
   build hash identically). Dispatch remains reflection-free.
3. **`PushExporter` + `meshd` MVP** - ✅ implemented: batching push exporter behind a
   `Sender` interface (lossy by design in every failure mode), span propagation
   (`SpanFromContext`/`Traceparent`) for cross-service joins, and the `meshd` collector
   (in-memory store, register/heartbeat/traces ingest + `mesh:query:*` read models,
   consumer edges derived from trace parentage at query time) + the
   `examples/mesh-helloworld` demo, whose tests run the whole story over real HTTP.
4. **Mesh View** - ✅ implemented: a single self-contained page embedded in `meshd`
   (no JS framework, per the zero-dependency stance), polling `mesh:query:fleet`
   through the envelope endpoint.
5. **Spec promotion** - ✅ complete and **merged to the main repo's `main`**:
   `docs/specification/mesh.md` there is the normative contract (the Go port is that
   document's reference implementation), alongside the three conformance fixture files -
   `mesh-descriptor-cases.json` (schema derivation + hash properties),
   `mesh-trace-cases.json` (traceparent join/reject + invocation→status mapping,
   including the new `conformance:panic` canonical handler), and
   `mesh-collector-cases.json` (ingest/derivation/degradation sequences). All three are
   vendored into this repo's `conformance/` with runners in
   `conformance/mesh_conformance_test.go`, and pass.

   An important discovery during promotion: the .NET implementation already ships its own,
   independently-designed mesh visibility feature (`Benzene.Mesh.*` - pull aggregator over
   a hand-maintained registry, OpenAPI spec hashing, Tempo-derived topology, Mesh UI).
   The promoted spec's §9 maps the two designs (nothing there is discarded - it's all
   collector-side idiom), and the main repo's `work/service-mesh-roadmap-1.0.md` now
   carries the concrete .NET convergence plan: descriptor + trace middleware to pass the
   two required fixture files, optional aggregator ingest topics for the collector
   fixtures, then the cross-language fleet demo. Notably, three of that roadmap's own open
   gaps (topology edge derivation, staleness, the hand-maintained registry) are solved by
   adopting the promoted wire layer.

   The .NET side has since caught up: the main repo's `Benzene.Mesh.Wire` package
   implements the service-side wire layer (descriptor + schemas + hash, reserved topic,
   trace middleware with traceparent join and span propagation, lossy batching exporter)
   and passes the same `mesh-descriptor-cases.json`/`mesh-trace-cases.json` fixtures this
   port passes. The cross-language fleet demo ran for real against this repo's `meshd`: a
   C# service registered, heartbeated, traced, and called the Go greeter with a propagated
   traceparent - the collector derived the cross-language consumer edge from parentage
   alone. The one remaining .NET item (optional per spec §7) is the aggregator adopting
   the `mesh:register`/`mesh:heartbeat`/`mesh:traces` ingest topics to pass
   `mesh-collector-cases.json`, tracked in the main repo's service-mesh roadmap.

## 9. Open questions

- **Reserved topic naming**: `mesh` vs `mesh:describe` — the healthcheck precedent
  is a bare reserved id; multi-segment ids are conventional elsewhere (`mesh:*` for
  collector topics). Spec decision.
- **Schema dialect** - resolved by the Phase 2 implementation: a documented subset of
  the JSON Schema 2020-12 vocabulary describing the *marshaled* form (pointers →
  nullable, `json` tags → names/required, `[]byte` → base64 string, `time.Time` →
  date-time, interfaces and custom `json.Marshaler`s → unconstrained `{}`, recursion cut
  at the cycle). The exact mapping lives on `deriveSchema` in `mesh/schema.go` and is
  what must be promoted to the spec so other language ports derive compatibly.
- **Retention/aggregation in `meshd`**: raw events vs. pre-aggregated rings for the
  MVP in-memory store; what the pluggable store interface must support.
- **Auth between services and `meshd`**: MVP is header-based shared secret; anything
  richer (per-cloud identity federation) is explicitly out of MVP scope.
- **Consumer identity**: trace parentage names the calling *service* only when the
  caller is itself meshed; unmeshed callers appear as anonymous edges. Acceptable
  for v1?

## Sources

- [nOps — Multi-cloud management challenges 2026](https://www.nops.io/blog/what-are-the-challenges-to-multi-cloud-management/)
- [Synergy Labs — Multi-cloud strategy 2026](https://www.synergylabs.co/blog/multi-cloud-strategy-in-2026-avoid-vendor-lock-in-without-doubling-your-complexity)
- [flolive — Multi-cloud in 2026: architecture, challenges](https://flolive.net/blog/glossary/multi-cloud-in-2026-architecture-challenges-and-best-practices/)
- [ACM/Empirical Software Engineering — "Enjoy your observability": industrial survey of microservice tracing](https://dl.acm.org/doi/10.1007/s10664-021-10063-9)
- [IEEE Access — Observability in microservices: frameworks, challenges, deployment paradigms](https://ieeexplore.ieee.org/iel8/6287639/10820123/10967524.pdf)
- [Riftmap — Backstage alternatives: first ask why](https://riftmap.dev/blog/backstage-alternatives/)
- [Medium — Backstage backlash: why developer portals struggle](https://medium.com/@samadhi-anuththara/backstage-backlash-why-developer-portals-struggle-cb82d4f082e1)
- [Roadie — Strategies for a complete software catalog](https://roadie.io/blog/3-strategies-for-a-complete-software-catalog/)
- [Solo.io — A service mesh should not be complex](https://www.solo.io/blog/service-mesh-should-not-be-complex)
- [earezki — No, you don't need a service mesh](https://earezki.com/you-dont-need-service-mesh/)
- [Total Shift Left — API schema validation drift detection](https://totalshiftleft.ai/blog/api-schema-validation-catching-drift)
- [Medium — Contract drift & schema mismatch detection](https://medium.com/@gunashekarr11/contract-drift-schema-mismatch-detection-the-most-underrated-api-failure-in-modern-systems-c278a2914205)
