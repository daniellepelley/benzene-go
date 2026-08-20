# Go-idiom & DX review of benzene-go

A snapshot assessment of how well this port fits Go idioms and Go-developer expectations, and
where it still reads as C# transliterated into Go. It is a plan of record for the "tighten up the
offering" work, the counterpart of `PARITY.md` (which tracks *capability* parity with the .NET
reference) for *idiom* parity with the Go ecosystem.

Every recommendation is weighed against the same tension:

- **Cross-language consistency** — benzene is a multi-language architecture whose canonical design
  lives in the .NET port and the language-neutral
  [spec](https://github.com/daniellepelley/Benzene/tree/main/docs/specification). Conceptual parity
  across ports (Middleware/Pipeline, Container/Scope, Registry, `Result[T]`, Topic/Status,
  ApplicationBuilder, the reserved `/benzene/` surface) has real value.
- **Go idiom** — a Go developer opening this repo should feel it was written by a Go developer, not
  ported from C#. Friction here alienates the exact audience the Go port exists to win.

Each finding is classified:

- **(A) Adopt the Go idiom** — the divergence from .NET costs little cross-language value and the
  current form is un-Go-like. Change it.
- **(B) Keep .NET-consistent** — the current form is deliberate cross-language parity; document the
  divergence clearly so a Go dev reads it as intentional.
- **(C) Genuine tension** — trade-off presented, with a recommendation.

## Verdict

**Go-idiomatic in its bones, historically C#-flavored on its surface.** The hard architectural calls
are right for Go: generics only where they buy type safety (`Handler[TReq,TRes]`, `Result[T]`,
`GetService[T]`) and deliberately dropped for the type-erased `Registry`; `context.Context` for
cancellation; accept-narrow-interfaces / return-structs applied consistently; a zero-dependency root
with per-SDK satellite modules; standard-library-only tests with hand-written fakes (no
testify/mock). The testing harness (`benzenetest`) is *ahead* of the .NET reference — setup is
identical across every transport and only the one `Send*` call names the cloud. What read as "ported
from C#" was almost entirely ergonomic surface, addressed by the items below.

## Findings

Status is **Done** (the decision below is implemented), **Partly** (landed, with a named remainder),
or **Won't do** (the decision was to leave it).

| # | Finding | Where | Class | Priority | Status |
|---|---------|-------|-------|----------|--------|
| 1 | No godoc `Example` tests anywhere | whole module | A | High | **Partly** — 18 `Example*` functions now ship (root `Register`/`Result`/`GetService`/`RouterMiddleware`, `httpbinding.Handler`, `benzenetest`'s flagship flow, `saga`, `asyncapi`, `auth`, `cache`, `cloudevents`, `healthcheck`, `idempotency`, `ratelimiting`, `resilience`, `validation`). Remainder: the `ExampleSend*`-per-transport-family set, and an example for one self-hosted `Consumer` |
| 2 | Godoc leaks .NET identifiers / essay-length | all packages | A | High | **Done** — exported comments lead with the caller-facing sentence; cross-language rationale reads as deliberate context rather than name-dropping |
| 3 | Split constructor convention (options vs struct+`Validate()`) | self-hosted consumers/workers | A/C | High | **Done** — all six converged on struct fields + `Validate()`: `awssqs.Consumer`, `azureservicebus.Worker`, `azureeventhub.Consumer`, `azurecosmos.Worker`, `kafka.Consumer`, `rabbitmq.Consumer`. The options constructors are gone |
| 4 | `Result[T]`/`Status` not `(T, error)`; DI-lite `Container`/`Scope` | `result.go`, `scope.go` | B | High (doc) | **Done** — kept, and taught: README's "Handlers return `Result[T]`, not `(T, error)`" section |
| 5 | `*Decorator` sender naming (GoF/C# term) | `client/*`, `mesh`, `diagnostics` | A | Medium | **Done** — `client.WithRetry`, `client.WithCorrelationID`, `mesh.WithTraceContext`, `diagnostics.WithTraceContext` |
| 6 | `Send*` shape variation undocumented | `benzenetest` | C→doc | Medium | **Done** — the four shape families are documented in `benzenetest/README.md`; the signatures stay as the wire dictates |
| 7 | `responseevents` not dogfooded through the harness | `responseevents` | C | Medium | **Done** — `responseevents/harness_test.go` |
| 8 | `gcp-cloudrun-helloworld` test uses raw `httptest` | example test | A | Medium | **Done** — resolved the second way the finding allowed: the raw `net/http` path is kept *because* it is the point of the Cloud Run example, and the test now says so and points at the sibling example that uses the harness |
| 9 | `App[TConfig]` / `ApplicationBuilder` `Use*` redundancy | `app.go` | C | Medium | **Partly** — `App` kept and repositioned, and `UseDefaultPipeline` earns its place as a shorthand. Remainder: `UsePipeline`/`UseReservedNames` still duplicate the exported fields, so there are still two ways to say the same thing |
| 10 | Naming polish: `CreatedResult`→`Created`, predicate cluster | `result.go`, `status.go` | A | Low | **Done** — `benzene.Created`, symmetrical with `Ok`/`Accepted`/`Updated` |
| 11 | "table-driven where the shape allows" claim overstated | `CLAUDE.md` | B | Low | **Done** — the wording now says table-driven where cases share a shape, named per-scenario where failure paths have distinct setup, and to match the package you are in |
| 12 | Module path ≠ package name forces import alias | `go.mod` | C | Low | **Won't do** — decided to leave; the README import snippet shows the `benzene "…/benzene-go"` alias so it reads as intentional |

## Detail & decisions

### 1 — Godoc `Example` tests (A, High)
Go treats `Example` functions as first-class, compile-checked documentation that renders on
pkg.go.dev. For a port whose headline is testability, their absence was the loudest "not written by
a Go dev" signal. **Decision: adopt.** Add `Example`s for the core flow (`Register`, `App.Run`,
`httpbinding.Handler`, one `Consumer`) and `benzenetest`'s flagship ingress→egress flow, plus an
`ExampleSend*` per transport family.

### 2 — De-C#-ify godoc (A, High)
Exported-symbol comments carried .NET identifier name-drops ("mirrors the .NET
`BenzeneResultStatus`"). Idiomatic godoc opens with a terse caller-facing sentence.
**Decision: adopt.** Trim exported-symbol comments to what the caller needs; relocate cross-language
rationale to a package-level "Cross-language notes" block. Zero parity cost.

### 3 — Constructor convention (A on consistency; C on direction; High)
Self-hosted consumers/workers doing the identical job disagreed on construction: functional options
(`awssqs.NewConsumer`, `azureservicebus.NewWorker`) vs exported struct + `Validate()`
(`kafka`, `rabbitmq`, `azureeventhub`). Both are individually idiomatic; the *split* is the problem.
For a couple of required fields plus a few optionals with good zero values, struct-fields +
`Validate()` is the more idiomatic Go (mirrors `http.Server`/`http.Client`). **Decision: converge on
struct + `Validate()`** for the self-hosted consumers/workers (a deliberate pre-1.0 API change for
`awssqs`/`azureservicebus`).

### 4 — Two deliberate wince-points, documented not changed (B, High-doc)
`Handler` returns `Result[T]` (a wire-contract `Status` vocabulary shared by every port), not
`(T, error)` — this keeps a batch consumer from crashing on one bad message and is core
cross-language identity. The `Container`/`Scope` DI-lite is a named spec concept. **Decision: keep
both, teach both** — a README section on why handlers return `Result[T]`, and the *typed-key*
`Container`/`Scope` pattern (with "capture singletons in the handler closure" as the preferred
plain-Go path) rather than a stringly-typed key.

### 5 — `*Decorator` → `With*` (A, Medium)
The wrap-a-`Sender`-return-a-`Sender` pattern is idiomatic; the `XDecorator` *name* is a GoF/C# term.
**Decision: adopt.** Rename `RetryDecorator`→`WithRetry`, `CorrelationDecorator`→`WithCorrelationID`,
`TraceContextDecorator`→`WithTraceContext` (pre-1.0). Symbol names carry no parity cost.

### 6 — `Send*` shape families (C→doc, Medium)
The `Send*` signatures vary (headers present/absent, positional keys, return types) — this is
correct wire fidelity (a DynamoDB record has no header channel; S3 delivers metadata not contents),
not sloppiness. **Decision: document, don't homogenize** — a "shape families" note
(queue/HTTP/stream/fan-in) in `benzenetest/README.md`.

### 7 — Dogfood `responseevents` through the harness (C, Medium)
`responseevents` is a literal ingress→handler→egress feature; its unit tests are fine but the most
copy-worthy adopter scenario (assert the republished event via `FakeMessageSender`) wasn't shown.
**Decision: add one harness-based feature test.**

### 8 — Align the `gcp-cloudrun-helloworld` test (A, Medium)
It tested the same greet handler via raw `httptest` instead of the harness, teaching a second idiom.
**Decision: align to `benzenetest` (or comment the deliberate contrast).**

### 9 — `App[TConfig]` / `ApplicationBuilder` `Use*` (C, Medium)
`App`'s three-closure lifecycle buys real test parity (same composition root in `main` and in
`benzenetest.NewHost`), so it stays — but docs should show the plain-`main` wiring as the simple
default. The `ApplicationBuilder` `Use*` setters duplicate its exported fields; prefer one way.
**Decision: keep `App`, reposition in docs; simplify the builder surface.**

### 10 — Naming polish (A, Low)
`CreatedResult`→`Created` for symmetry with `Ok`/`Accepted`/`Updated`; document the
`Result.IsSuccessful` vs `Status.IsFailure` split so the predicate cluster reads clearly.
**Decision: adopt the safe renames pre-1.0.**

### 11 — Correct the convention wording (B, Low)
Reality is a sound mix of table-driven and named per-scenario tests. **Decision: soften the
repo-guide claim to match** (the claim lives in `CLAUDE.md`, this repo's project guide).

### 12 — Module path vs package name (C, Low)
`…/benzene-go` imports as package `benzene`, forcing an alias. **Decision: leave** (a rename is
disruptive); note the alias in the README import snippet so it reads as intentional.

## Strengths to preserve (do not "fix")

- Standard-library-only tests; hand-written `fakeXxx` spies; **no** testify/mock/assert.
- The single-`Send*` specialization (setup identical across ~15 transports) — ahead of the .NET
  `Build*` step.
- Free-function `Send*` correctly resolving that Go generics can't be methods and that SDK helpers
  live in their own modules.
- Zero-dep root + per-SDK modules; `context.WithoutCancel` settlement; double-checked locking in
  `scope.go`; unexported context-key types; `any` over `interface{}`; no `benzene.BenzeneX` stutter.
