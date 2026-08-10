# circuitbreaker

A Benzene pipeline middleware that wraps a **circuit breaker** around the downstream pipeline.

While the breaker is **closed**, invocations pass through and failures are counted. Once failures
cross the trip threshold the breaker **opens** and further invocations short-circuit immediately
(fail fast) without touching the downstream, until the breaker's timeout lets it try again
(**half-open**). It is the circuit-breaker slice of `Benzene.Resilience.Polly` and the sibling of
the zero-dependency [`resilience`](../resilience) package (retry + timeout).

Retry re-invokes a call that just failed; a circuit breaker does the opposite — it *stops* calling a
dependency that keeps failing, giving a struggling downstream room to recover instead of hammering
it, and letting callers fail fast instead of piling up on a doomed request.

## Dependency and its own module

The breaker is the piece that genuinely wants a battle-tested library, so this package depends on
[`github.com/sony/gobreaker/v2`](https://github.com/sony/gobreaker). Like `awssqs` and the other
dependency-carrying bindings, it lives in **its own Go module** so that dependency does not spread to
the zero-dependency root module. You take the dependency only if you import this package.

## Usage

Construct a `gobreaker.CircuitBreaker` with the trip policy you want, then wrap it as middleware:

```go
import (
	"github.com/daniellepelley/benzene-go/circuitbreaker"
	"github.com/sony/gobreaker/v2"
)

// Open after 5 consecutive failures; stay open 30s, then probe (half-open).
cb := gobreaker.NewCircuitBreaker[struct{}](gobreaker.Settings{
	Name:        "payments",
	MaxRequests: 1,                // half-open probes allowed at once
	Timeout:     30 * time.Second, // open -> half-open after this
	ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= 5 },
})

app.Use(circuitbreaker.Middleware(cb))
```

The breaker's trip threshold, open-state timeout, and half-open probe count all live on
`gobreaker.Settings` (which you own). The middleware adds the Benzene-specific behavior: what counts
as a failure, and what a short-circuit looks like on `ic.Result`.

`T` (here `struct{}`) is gobreaker's result type parameter. It is irrelevant to this middleware — the
handler writes `ic.Result`, not a returned value — so any type works; use `struct{}`.

## Failure model

The Go pipeline expresses a handler outcome two ways, and the breaker can trip on **both**, mirroring
`resilience`'s two retry triggers:

- a genuine **error** from `next()` counts as a failure to the breaker **and** propagates up
  unchanged;
- a `next()` whose `ic.Result` matches the configurable `WithTripOnResult` predicate counts as a
  failure to the breaker, but the result stays on `ic.Result` and the middleware returns `nil` — that
  result *is* the Benzene outcome, not a transport error;
- anything else (a success, or a non-matching result) does not count as a failure.

**The default is `TripOnServerError`** — only the dependency-health statuses (`service-unavailable`,
`timeout`, `unexpected-error`) trip the breaker. Client-error results (`bad-request`,
`validation-error`, `unauthorized`, `forbidden`, `not-found`, `conflict`) deliberately **do not**:
the breaker sits on the inbound pipeline but is meant to isolate an unhealthy *dependency*, so a
flood of malformed or unauthorized client requests must not open it and shed all traffic — client
mistakes taking the service down is the opposite of a breaker's purpose. Pass
`WithTripOnResult(TripUnsuccessful)` if you really do want every unsuccessful result to trip it.

## Short-circuit

When the breaker is open (or half-open at its probe limit), the call short-circuits: the middleware
sets `ic.Result` to a fail-fast result and returns `nil` **without invoking the downstream** — a
short-circuit is a Benzene result, not a transport error (exactly like `ratelimiting`'s
too-many-requests rejection). Defaults to `StatusServiceUnavailable` with a
`"circuit breaker is open"` message.

## Options

| Option | Purpose | Default |
| --- | --- | --- |
| `WithTripOnResult(func(benzene.ResultInfo) bool)` | What counts as a failing result | `TripOnServerError` |
| `WithOpenStatus(benzene.Status)` | Status of the fail-fast short-circuit result | `StatusServiceUnavailable` |
| `WithOpenMessages(...string)` | Error messages on the short-circuit result | `"circuit breaker is open"` |

Ready-made trip predicates:

- `TripOnServerError` — trip only on the dependency-health statuses (`service-unavailable`,
  `timeout`, `unexpected-error`); client-error results never open the breaker (**the default**).
- `TripUnsuccessful` — trip on *any* result the pipeline treats as unsuccessful, client errors
  included. Use only when every unsuccessful outcome reflects the protected dependency's health.
- `TripOnStatus(statuses...)` — trip only on a custom set of statuses, e.g.
  `TripOnStatus(benzene.StatusServiceUnavailable, benzene.StatusTimeout)`.

> The middleware relies on gobreaker's **default** failure classification (a non-nil error is a
> failure): a result-based trip is signalled with an internal sentinel error. If you set a custom
> `gobreaker.Settings.IsSuccessful`, it must still treat a non-nil error as a failure, or result-based
> tripping is silently defeated.

## Placement

Register the breaker **above** the outbound/port calls it should protect — it must sit between the
caller and the dependency whose failures it counts. Compose it with `resilience` by ordering:

- Put **retry below the breaker** (`breaker → retry → the call`) so a burst of retries against a dead
  dependency still counts toward tripping, and once open, the breaker fails fast before retry even
  starts.
- A **timeout belongs below the breaker** too, so an elapsed deadline is one of the failures that can
  trip it.
