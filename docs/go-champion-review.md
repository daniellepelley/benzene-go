# Go-champion review of benzene-go

A whole-implementation idiom + developer-experience assessment of the benzene-go port as it
stands on the current `main`-based branch, produced by the **go-champion** charter
(`.claude/agents/go-champion.md`). It is the implementation-and-DX plan-of-record, sibling to
`docs/go-idioms-review.md` (the earlier idiom snapshot, most of whose structural items are now
done — see "What the prior review already fixed") and `PARITY.md` (capability parity).

Every finding is classified **A** (adopt the Go idiom), **B** (keep the cross-language contract,
document why), or **C** (genuine tension, recommendation given), with a severity
(Blocker / High / Medium / Polish), grounded in a named Go rule (Effective Go, Go Code Review
Comments, a stdlib precedent) for "not idiomatic" and in the spec (`/home/user/Benzene/docs/
specification/**`) for "is/isn't a contract".

## Verdict

**This is an idiomatic, enjoyable, genuinely well-built Go port — the surface is clean and the
load-bearing decisions are right.** `gofmt -l .` is empty, `go vet` is clean across every module,
`go build` and `go test -race` are green across the root plus all ~20 nested modules (all
verified this session). The conventions that everything else copies — `Handler[TReq,TRes]` as a
plain func, `Result[T]` + type-erased `ResultInfo` recovered via an interface (not reflection),
`context.Context` first and never stored, accept-interfaces/return-structs at the `client.Sender`
seam, `wire` kept dependency-free, one third-party dep per satellite module, `With*` options and
`struct + Validate()` workers used **consistently** across the satellites — are exactly what a
seasoned Go developer expects, and the doc comments teach the two real surprises (`Result[T]` over
`(T, error)`; DI-lite `Container`) instead of hiding them. There are **no Blockers and no High
correctness/safety findings**. What remained was DX polish: a storefront gap (few runnable
`Example`s), one mis-named adapter (`healthcheck.CheckFunc`), one inconsistent nil-guard
(`App.GetConfiguration`), and the flagship examples teaching a DI-key shortcut the docs
themselves warn against. All but the storefront gap have since landed - each finding below
carries its own **Status** line, and A2 is the one still substantially open. Landing verdict:
**IDIOMATIC & ENJOYABLE (polish findings filed)**.

(Note on scope: the task brief referenced a `port-quality-standards.md` in the spec — that file
does not exist in `/home/user/Benzene/docs/specification/`; the grounding here is
`design-principles.md`, `core-concepts.md`, and `wire-contracts.md`, which do.)

## What the prior review (`go-idioms-review.md`) already fixed

Verified done this session, so they are **not** re-filed below:

- **#3 constructor convergence** — every self-hosted consumer/worker now uses exported
  `struct + Validate() + Run(ctx)`: `awssqs.Consumer`, `azureservicebus.Worker`,
  `kafka.Consumer`, `rabbitmq.Consumer`, `azureeventhub.Consumer`, `azurecosmos.Worker`. The
  split the prior review flagged is gone. (Outbound clients uniformly use `struct + NewClient(...)`
  — a deliberate, defensible contrast: clients take 1–2 required deps as constructor params,
  workers carry many fields with useful zero values. Left as-is.)
- **#5 `*Decorator` → `With*`** — no `Decorator` identifier remains anywhere in non-test code;
  `client.WithCorrelationID` / `WithRetry` and the `mesh`/`diagnostics` `WithTraceContext` are the
  names.
- **#10 `CreatedResult` → `Created`** — the `Ok`/`Created`/`Accepted`/… cluster in `result.go` is
  symmetric; no `*Result`-suffixed constructor remains.
- **Options naming** — `With*` is used uniformly across every package that takes functional
  options (spot-checked `resilience`, `auth`, `awssqs`, `mesh`, `circuitbreaker`,
  `cloudservice`, …).

---

## A — Adopt the Go idiom

### A1 — `healthcheck.CheckFunc` is a struct wearing a `Func` name, with a stuttering field
**What & where:** `healthcheck/healthcheck.go:52-58`.
```go
type CheckFunc struct {
    CheckName string
    Fn        func(ctx context.Context) CheckResult
}
func (f CheckFunc) Name() string                          { return f.CheckName }
func (f CheckFunc) Check(ctx context.Context) CheckResult { return f.Fn(ctx) }
```
Used in the flagship example as `healthcheck.CheckFunc{CheckName: "memory", Fn: func(...) {...}}`
(`examples/helloworld/main.go:89`).

**Idiom:** Go's `…Func` suffix is a term of art — `http.HandlerFunc`, `http.FileServer`'s handler
adapters — meaning "a **func type** that adapts a plain function to a single-method interface"
(Effective Go, "Interface conversions"; Code Review Comments "interface pollution"/naming). A
`CheckFunc` that is a two-field **struct** violates that expectation on sight, and
`CheckFunc{CheckName: …}` stutters (Code Review Comments, "Package Names / avoid stutter"). Because
`Check` has *two* methods (`Name`, `Check`), a bare func type genuinely can't carry the name — but
the answer is a constructor, not a mis-named struct.

**Pull & landing:** Pure Go idiom, zero contract weight — `Check` is the concept, its adapter is a
Go spelling choice. Land on the accept-func/return-interface idiom: add
`func NamedCheck(name string, fn func(context.Context) CheckResult) Check`, the ready-made way to
turn a name + closure into a `Check`. Keep the struct temporarily if you want a non-breaking
window, but the example and docs should show `NamedCheck`.

**Severity:** Medium (it's in the very first `Configure` a newcomer copies).
**Fix (recommended):** add `NamedCheck`; migrate `examples/helloworld` and any other `CheckFunc{}`
call sites; consider renaming the struct's `CheckName` field to `Name`… (blocked by the method
name) — prefer deleting the struct once `NamedCheck` lands.
**Status: DONE.** `healthcheck.NamedCheck(name, fn)` is the adapter, and the mis-named struct is
gone.

### A2 — The `Example` storefront: most of it filled, the transport half still bare
**What & where:** when this review was written, only 4 `Example*` functions existed module-wide.
There are now **18**, and most of the named gaps are closed: `Register`, `Result`, `GetService` and
`RouterMiddleware` in the root `example_test.go`; `httpbinding.Handler`; the `benzenetest`
ingress→egress flow; and the building blocks — `healthcheck.Middleware`, `auth.BasicAuth`,
`resilience.Fallback`, `idempotency.Middleware`, `ratelimiting.Middleware`, `cache.GetOrLoad`,
`validation`, `saga`, `asyncapi`, `cloudevents`.

What is still bare is the transport half, which is the half a DX-branded port is judged on: there
is **no `ExampleSend*` for any transport family** (queue / HTTP / stream / fan-in), and no `Example`
on a self-hosted `Consumer`. A reader landing on `awssqs` or `kafka` on pkg.go.dev still sees no
runnable code.

**Idiom:** Go treats `Example` functions as first-class, **compile-checked** documentation that
renders directly on pkg.go.dev (`testing` package docs; Effective Go). For a library that sells
itself on "write your handler once, host anywhere, test it the same way everywhere," pkg.go.dev is
the storefront — and "test it the same way everywhere" is exactly the claim the missing
`ExampleSend*` set would demonstrate in one screen. This was the prior review's #1 (rated High);
the core-flow half is now done, the transport half is not.

**Pull & landing:** Pure idiom/DX, no contract dimension. Adopt.

**Severity:** Medium (down from High — the core flow now has examples; what is left is breadth).
**Fix (recommended):** one `ExampleSend`-family per transport shape (queue / HTTP / stream / fan-in
— reuse the "shape families" framing already in the prior review's #6, and already written up in
`benzenetest/README.md`), plus one `Example` on a self-hosted `Consumer`. These double as
compile-checked usage tests.
**Status: PARTLY DONE.** 4 → 18 `Example*` functions; the `ExampleSend*` set remains open.

### A3 — `App.GetConfiguration` is the only lifecycle phase that isn't nil-tolerant
**What & where:** `app.go:29`. `Run()` calls `config := a.GetConfiguration()` unconditionally,
while `ConfigureServices` (line 33) and `Configure` (line 38) are both `!= nil`-guarded. A caller
who builds an `App[struct{}]` and omits `GetConfiguration` gets a bare nil-func-deref panic with no
message.

**Idiom:** "Make the zero value useful" / least surprise (Effective Go). The three phases read as a
uniform optional-hooks set (the doc comment even says "ConfigureServices and Configure are
optional… may leave either nil"); singling out `GetConfiguration` to panic on nil is an
inconsistency a Go developer won't predict, and for `App[struct{}]` the config closure is pure
boilerplate (`func() struct{} { return struct{}{} }`) — see `examples/helloworld/main.go:78`.

**Pull & landing:** Idiom, no contract weight (`TConfig` is explicitly application-defined,
core-concepts §7). Adopt: if `GetConfiguration == nil`, use `var config TConfig` (the zero value).
Non-breaking — it only turns a panic into a sensible default and removes boilerplate.

**Severity:** Medium.
**Fix (recommended, maintainer's call since it's public behavior):** nil-guard
`GetConfiguration` in `Run()` to fall back to the zero `TConfig`, and update the doc comment to
list all three phases as optional.
**Status: DONE.** `Run()` starts `var config TConfig` and only calls `GetConfiguration` when it is
non-nil; the doc comment now says all three phases are optional.

### A4 — Doc comments still carry .NET identifiers / spec-section density in the lead sentence
**What & where:** ~91 non-test doc-comment lines reference `.NET`, `mirrors`, or a
`` `Benzene.X` `` identifier (e.g. `healthcheck/healthcheck.go:28`, `mesh/wire.go:13`,
`awsdynamodb/attributevalue.go:12`). Most are *mid-comment* interop notes and are fine (see B4);
the wart is only where an exported symbol's **first sentence** leads with a spec citation or a .NET
name instead of a caller-facing statement.

**Idiom:** godoc convention — a doc comment "should begin with the name … and be a complete
sentence" describing what the caller gets (Effective Go, "Commentary"; Code Review Comments,
"Doc Comments"). Cross-language rationale belongs after that sentence or in the package doc.

**Pull & landing:** Idiom on placement; the *content* is often a real contract note (keep it, per
B4). Adopt the ordering: caller-facing first sentence, rationale below. This is the prior review's
#2, partially addressed.

**Severity:** Polish (do opportunistically; low value-per-edit, high edit count).
**Fix (recommended):** when touching a package for another reason, ensure its exported symbols'
first sentences are caller-facing; leave the interop notes in place lower down.

---

## B — Keep the cross-language contract (documented, correct as-is)

These look un-Go-ish to a newcomer but are real shared contracts; they are already documented, and
this review affirms them so the next reader doesn't "fix" them.

- **B1 — `Handler` returns `Result[T]`, not `(T, error)`** (`registry.go:14`, `result.go`). The
  `Status` vocabulary is the wire contract every port and transport shares (wire-contracts.md §3);
  value-not-error is also what lets a batch consumer turn one bad message into a `bad-request`
  rather than crash the batch. **Keep.** Well taught in `README.md` ("Handlers return `Result[T]`,
  not `(T, error)`") and `result.go`.

- **B2 — `Status` is a kebab-case wire string with PascalCase Go identifiers** (`status.go`). The
  string values (`"not-found"`, `"validation-error"`) are the case-sensitive wire contract; the
  `Status`-typed constants are the Go idiom over them. **Keep.** The divergence is explicitly
  documented at `status.go:3-14`.

- **B3 — DI-lite `Container`/`Scope` with free-function `GetService[T]`** (`scope.go`). A named
  spec concept (core-concepts.md §8), and free functions are *forced* by the language (Go methods
  can't be generic), not a stylistic import from C#. **Keep.** The double-checked-locking rationale
  and the "capture singletons in the handler closure" preferred path are documented at
  `scope.go:179-186` and in `CLAUDE.md`.

- **B4 — Cross-language interop notes in doc comments** (the ~91 refs behind A4). Where a comment
  says a topic/shape "matches the .NET reference's `BenzeneTopic.Mesh`" it is documenting a **wire
  interop requirement** (two ports must emit the identical reserved string), which the charter says
  to keep and mark deliberate. **Keep the content**; A4 only asks that it not be the *lead*
  sentence.

---

## C — Genuine tension (recommendation given)

### C1 — Every flagship example teaches a stringly-typed DI key the docs warn against
**What & where:** `examples/helloworld/main.go:40` (`const greetingCounterKey = "greeting-counter"`),
and identically `examples/http-helloworld`, `examples/kafka-helloworld`,
`examples/opentelemetry-helloworld` (`const greeterKey = "greeter"`). Meanwhile `scope.go:9-13`
says "callers should use a package-level unexported **type** or a stable string constant … to avoid
collisions," and the prior review's #4 decided to "teach the typed-key pattern … rather than a
stringly-typed key."

**Idiom / tension:** `serviceKey = any`, so a bare untyped `string` key works, and in a
single-package example there is no collision. But the canonical examples are what adopters copy
into multi-package services, where two packages both registering `"greeter"` in the one app-global
`Container` silently collide — the exact footgun the unexported-zero-size-type idiom exists to
prevent (the same reasoning as context keys: Code Review Comments, "Contexts"; the pattern
`scope.go` already uses internally with `scopeContextKey struct{}`). The tension is real:
strings read simpler in a demo; typed keys model the safe pattern.

**Pull & landing:** Land on modelling the safe pattern in at least the flagship
`examples/helloworld`: `type greetingCounterKey struct{}` used as the key (or a package-scoped
typed constant), with a one-line comment on the collision boundary; keep the string form only if a
sentence explains why it's safe here. Consistency across the examples matters more than the demo
being one line shorter.

**Severity:** Medium.
**Fix (recommended):** switch the flagship example to an unexported key type; add a short "DI keys"
note to `docs/message-handlers.md` (or wherever DI is taught) showing the typed key as the default
and "capture the singleton in the handler closure" as the even-simpler path.
**Status: DONE.** The examples use unexported zero-size struct keys (`greetingCounterKey{}`,
`greeterKey{}`), each with the collision-boundary comment.

### C2 — Two wiring styles are shown before either is explained
**What & where:** the `README.md` Quickstart hand-builds
`&benzene.ApplicationBuilder{Registry, Container, Pipeline}` directly (README lines ~40-50), while
`examples/helloworld` and every other example wire through the three-phase `App[TConfig]`
lifecycle. Both are legitimate (the prior review's #9: `App` buys test parity, the plain builder is
the simple default), but a newcomer meets two composition idioms in the first five minutes.

**Pull & landing:** Keep both — they serve different needs — but signpost. The Quickstart should
carry one line: "this is the no-`App` form; `examples/` use the `App` lifecycle so a test boots the
exact wiring that ships (see `benzenetest`)." Uniformity of *explanation*, not of mechanism.

**Severity:** Polish.
**Fix (recommended):** one sentence in the README Quickstart pointing at the `App` form and why.
**Status: DONE.** The Quickstart now says it wires the builder directly because that is the
shortest thing that runs, and points at the three-phase `App[TConfig]` lifecycle and
`examples/helloworld` for the real-service form.

---

## Strengths to preserve (do not "fix")

- Type-erased `Registry` recovered via `ResultInfo` (`result.go`, `registry.go`) — reflection used
  **only** at startup for schema derivation, never on the dispatch path.
- `wire` as a dependency-free island (`wire/envelope.go:1-7`) and the one-third-party-dep-per-module
  discipline (`go.work` + per-module `go.mod`).
- The "never return a Go error to the transport" boundary held uniformly
  (`router.go:44-56`, `envelope/dispatch.go:24-27`, `httpclient` mapping every failure to
  `service-unavailable`) — a handler panic becomes a `Result`, not a crash.
- Sentinel `Err*` values with `%w` wrapping where callers branch (`auth/jwt.go:56-67`); `%v` used
  only for panic-recover values, correctly (`router.go:47`, `saga/step.go:95`).
- Consistent `struct + Validate() + Run(ctx)` workers and `struct + NewClient` outbound clients;
  `With*` options everywhere; unexported context-key types; no `benzene.BenzeneX` stutter.

## Highest-value next change

**A2 — add the `Example` functions.** pkg.go.dev is the storefront for a port whose entire pitch is
"idiomatic, testable Go," and it currently renders almost no runnable usage. A handful of
compile-checked `Example`s (core flow + one per transport shape + the `benzenetest` flow) is the
single change that most improves the first-impression DX, and it costs nothing in API surface or
cross-language fidelity. Pair it with A1 (`NamedCheck`) so the first example a newcomer reads is
already idiomatic.
