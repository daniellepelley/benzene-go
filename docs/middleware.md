# Middleware

Every transport event a Benzene service handles — an HTTP request, a queue message, a cloud
function invocation — flows through the same thing on its way to a handler and back: a **pipeline**
of middleware. Middleware is where cross-cutting concerns live (logging, tracing, health checks,
CORS, and the routing that actually reaches your handler), kept out of the handler itself so the
handler stays a plain function of request to [`benzene.Result[T]`](message-result.md).

The pipeline is one of the four language-neutral Benzene concepts — see
[Core concepts §4](https://benzene.app/docs/specification/core-concepts.html) for the full model.
This page shows the Go shape: the middleware type, how a pipeline composes, why order matters, how
to write your own, and the built-in middleware Benzene-Go ships.

## The middleware type

A middleware is a single function type, defined in the root `benzene` package:

```go
type Middleware func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error
```

Three arguments and one return:

- **`ctx`** carries cancellation, deadlines, and invocation-scoped values. Everything that would be
  a "cancellation parameter" in another framework rides here instead, so the signature is identical
  across transports that have no cancellation concept at all.
- **`ic`** is the `*benzene.InvocationContext` — the mutable state of *this one* invocation: the
  resolved `Topic`, the inbound `Headers`, the raw `Request`, the per-invocation DI `Scope`, the
  `Result` once a handler has run, and `ResponseHeaders` for outbound headers. Middleware reads and
  writes it freely.
- **`next`** continues the chain. Calling `next(ctx)` runs the rest of the pipeline; *not* calling
  it stops the chain there.
- The **`error`** return is a pipeline-level (infrastructure) failure, not an application outcome.
  Application outcomes — a bad request, a missing handler, even a handler panic — are a `Result` on
  `ic.Result`, never a Go error (see [The terminal router](#the-terminal-router-routermiddleware)).

## The onion model

Middleware composes as an onion: each layer runs on the way *in*, calls `next`, then runs on the way
*out* as the call unwinds. Because a middleware wraps its call to `next`, the first-registered
middleware is the **outermost** layer — it runs first on the way in and last on the way out:

```go
func timing(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
    start := time.Now()          // on the way in
    err := next(ctx)             // run everything inside this layer
    log.Printf("%s took %s", ic.Topic.ID, time.Since(start)) // on the way out
    return err
}
```

For a pipeline of `first` then `second`, the observable order is:

```
first:before → second:before → second:after → first:after
```

A middleware that returns without calling `next` **short-circuits** the pipeline: everything after
it — including the handler dispatch, if the router was registered later — does not run. That is the
mechanism behind health-check interception: the health-check middleware answers the reserved topic
and simply doesn't call `next`, so the router never sees it. An `error` returned anywhere propagates
straight back up through the outer layers unchanged.

## Composing a pipeline

`benzene.NewPipeline` takes middleware in registration order and returns a `*Pipeline`:

```go
func NewPipeline(middlewares ...Middleware) *Pipeline
```

```go
pipeline := benzene.NewPipeline(
    logging.Middleware(nil),                 // outermost: sees every invocation
    healthcheck.Middleware(checks),          // intercepts the reserved health topic
    benzene.RouterMiddleware(registry),      // terminal: resolves the topic and dispatches
)
```

You rarely call `NewPipeline` directly in application code — the composition root's `Configure`
phase hands the pipeline to the builder with `builder.UsePipeline(benzene.NewPipeline(...))`, as the
[getting-started guide](getting-started.md#3-compose-the-app) shows. `Pipeline.Run(ctx, ic)`
executes the whole onion exactly once for one `InvocationContext`; one transport event is exactly
one `Run` call, and arranging that (including one `Run` per message in a batch) is the transport
binding's job, not yours.

### Why order matters

Because registration order *is* execution order, where a middleware sits changes what it can see and
do:

- **Observability middleware goes outermost.** `logging.Middleware` and `diagnostics.Middleware`
  should run before anything that might short-circuit, so they observe *every* invocation — including
  intercepted health checks — and measure the full duration.
- **Interceptors go before the router.** `healthcheck.Middleware` must run before
  `RouterMiddleware`, so it can short-circuit the reserved topic before the router tries to resolve
  it.
- **The router goes last.** It is the terminal middleware that reaches your handler; anything that
  needs to run *before* the handler must be registered ahead of it, and anything that inspects the
  `Result` must be registered behind it (the `Result` doesn't exist until the router populates it).

## The terminal router (`RouterMiddleware`)

`benzene.RouterMiddleware` is the middleware that actually reaches your handlers. It resolves
`ic.Topic` against the registry and dispatches to the matching handler, storing the outcome on
`ic.Result`:

```go
func RouterMiddleware(registry *Registry) Middleware
```

It is an **ordinary middleware**, not a hard pipeline terminator — it calls `next(ctx)` after
dispatching, so middleware registered *after* the router still runs (it just runs with `ic.Result`
already populated). By convention it is registered last, per
[Core concepts §4](https://benzene.app/docs/specification/core-concepts.html).

Crucially, the router never returns a Go `error` for an application-level outcome. Every case is
mapped to a `Result` on `ic.Result` instead, so a caller uniformly reads `ic.Result` rather than
distinguishing "no handler" from "handler ran":

| Situation | Result status |
| --- | --- |
| Topic is missing/empty | `ValidationError` |
| No handler registered for the topic | `NotFound` |
| Request payload can't be converted to the handler's type | `BadRequest` |
| Handler panics | `ServiceUnavailable` (recovered — a panic must not crash the transport) |
| Handler runs | whatever `Result` it returns |

Before invoking a handler, the router attaches the invocation and its DI `Scope` to the handler's
`ctx`. That is how a handler resolves scoped or transient dependencies with
`benzene.ScopeFromContext(ctx)` + `benzene.GetService[T]`, and sets an outbound header with
`benzene.SetResponseHeader(ctx, name, value)`, without any of that widening the handler signature.

## Writing your own middleware

A middleware is just a function of the `Middleware` type — or, more usefully, a constructor that
closes over configuration and returns one. Here is a small header-stamping middleware that adds an
outbound header to every response, on the way out:

```go
package main

import (
    "context"

    benzene "github.com/daniellepelley/benzene-go"
)

// StampVersion returns a middleware that tags every response with an X-Service-Version header.
func StampVersion(version string) benzene.Middleware {
    return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
        err := next(ctx)                              // let the rest of the pipeline run first
        ic.SetResponseHeader("X-Service-Version", version) // then stamp the response on the way out
        return err
    }
}
```

Register it in the pipeline like any other middleware:

```go
builder.UsePipeline(benzene.NewPipeline(
    StampVersion("1.4.0"),
    benzene.RouterMiddleware(builder.Registry),
))
```

Things to keep in mind, all direct consequences of the type:

- **Always propagate `next`'s return** unless you deliberately want to swallow an infrastructure
  error — return the `err` you get back, don't drop it.
- **To short-circuit, return without calling `next`** (optionally setting `ic.Result` first, the way
  `healthcheck.Middleware` does). To run code before the handler, do it before `next`; to run code
  after, do it after.
- **Pass `ctx` (or a derived context) to `next`.** If you enrich the context — a new span, a value —
  pass the enriched context onward so downstream layers and the handler see it.
- **Mutate `ic` freely.** Adding an inbound header before the router, or inspecting/replacing
  `ic.Result` after it, are both normal.

## Built-in middleware

Benzene-Go ships a small set of ready-made middleware. The root module stays dependency-free, so
anything that needs a third-party dependency lives in its own Go module.

### `healthcheck.Middleware` — health-check interception

**Package:** `github.com/daniellepelley/benzene-go/healthcheck`

Intercepts the reserved `benzene:healthcheck` topic (`healthcheck.ReservedTopic`), plus any aliases
you pass, and short-circuits the pipeline with the aggregate health-check response — running every
check concurrently. Any other topic passes straight through to `next`. Register it before the router
so it can intercept before routing happens.

```go
func Middleware(checks []Check, aliases ...string) benzene.Middleware
```

```go
checks := []healthcheck.Check{
    healthcheck.NamedCheck("memory", func(context.Context) healthcheck.CheckResult {
        return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "memory"}
    }),
}

builder.UsePipeline(benzene.NewPipeline(
    healthcheck.Middleware(checks),
    benzene.RouterMiddleware(builder.Registry),
))
```

A `Check` is any type with `Name() string` and `Check(ctx) CheckResult`; `healthcheck.NamedCheck`
adapts a name + plain function when you don't need a dedicated type. Each check reports one of
`healthcheck.StatusOk`, `StatusWarning`, or `StatusFailed`. A healthy report becomes `benzene.Ok`
(HTTP 200); any failed check flips the aggregate to `ServiceUnavailable` (HTTP 503) while still
rendering the report body. A check that panics is recorded as `StatusFailed` rather than crashing the
endpoint. Behavior follows
[wire-contracts §5](https://benzene.app/docs/specification/wire-contracts.html).

### `logging.Middleware` — structured request logging

**Package:** `github.com/daniellepelley/benzene-go/logging`

Emits one structured `log/slog` line per invocation after the downstream chain finishes — topic (and
version, when versioned), the Benzene status, and the duration in milliseconds. It is deliberately
*not* tracing or metrics: no export, no dependency, just enough to answer "what ran, how long, and
how did it end" from stdout. Register it outermost so it sees every invocation, including intercepted
ones.

```go
func Middleware(logger *slog.Logger) benzene.Middleware
```

```go
pipeline := benzene.NewPipeline(
    logging.Middleware(nil), // nil uses slog.Default()
    healthcheck.Middleware(checks),
    benzene.RouterMiddleware(registry),
)
```

The level tracks the outcome: `Info` for a success status, `Warn` for a non-success status (the
errors travel in an `errors` attribute), and `Error` for a pipeline-level Go error — which is logged
and then propagated untouched, since logging observes but never absorbs. Passing `nil` uses
`slog.Default()`, so `logging.Middleware(nil)` works with or without a configured default logger.

### `diagnostics.Middleware` — OpenTelemetry tracing & metrics

**Package:** `github.com/daniellepelley/benzene-go/diagnostics` (a separate Go module — it depends on
the OpenTelemetry API)

Produces one OpenTelemetry span per invocation (named by topic, `SpanKind` server, joined to the
caller's trace via the inbound W3C `traceparent` header, tagged `benzene.topic`/`benzene.version`/
`benzene.status`), plus a `benzene.messages.processed` counter and a `benzene.message.duration`
histogram — both attributed by `topic`/`transport`/`result`, the cross-port
[observability conventions](https://github.com/daniellepelley/Benzene/blob/main/docs/guides/observability-conventions.md)
(`transport` is currently always `"<missing>"` — this port has no way yet to read a binding's
identity back). It depends on the OpenTelemetry *API* only, never the SDK: the application owns SDK
setup, and with no SDK installed the API's no-op defaults make the middleware free and silent.
Register it outermost so it observes every invocation.

```go
func Middleware(opts ...Option) benzene.Middleware

func WithTracerProvider(tp trace.TracerProvider) Option
func WithMeterProvider(mp metric.MeterProvider) Option
```

```go
pipeline := benzene.NewPipeline(
    diagnostics.Middleware(), // uses the ambient global providers
    benzene.RouterMiddleware(registry),
)
```

By default the middleware discovers the ambient global tracer/meter providers; pass
`diagnostics.WithTracerProvider` / `diagnostics.WithMeterProvider` to supply them explicitly. The
span's context is passed to `next`, so downstream middleware, handlers, and outbound clients see the
current span. The package also ships `diagnostics.TraceContextDecorator`, which is *not* a pipeline
middleware but an outbound `client.Sender` decorator — it injects the current span context as
`traceparent`/`tracestate` headers on outbound `Send` calls so a downstream Benzene service continues
the same trace.

### `cors.Middleware` — Cross-Origin Resource Sharing

**Package:** `github.com/daniellepelley/benzene-go/cors`

CORS is an HTTP-transport concern (the `Origin` header, preflight `OPTIONS`, `Access-Control-*`
response headers), so — unlike the three above — this is **not** a `benzene.Middleware` over the
pipeline. It is an ordinary `net/http` middleware that wraps an `http.Handler`, sitting in *front* of
whatever `httpbinding.Handler` produces:

```go
func Middleware(settings Settings, routes []httpbinding.Route) func(http.Handler) http.Handler
```

```go
corsSettings := cors.Settings{
    AllowedOrigins:   []string{"https://example.com"},
    AllowedHeaders:   []string{"Content-Type", "Authorization"},
    ExposedHeaders:   []string{"X-Total-Count"},
    AllowCredentials: true,
    MaxAge:           10 * time.Minute,
}

mux := http.NewServeMux()
mux.Handle("/", cors.Middleware(corsSettings, routes())(httpbinding.Handler(builder, routes())))
```

It computes `Access-Control-Allow-Methods` per path from the same route table you pass to
`httpbinding.Handler`, answers preflight `OPTIONS` directly, and adds the appropriate headers to
allowed cross-origin requests. `AllowedOrigins` entries match either as a full origin
(`"https://example.com"` — exact scheme + host + port) or a bare hostname (`"example.com"` — host
only); `"*"` allows any origin and echoes the request's own `Origin` back, so it's safe with
`AllowCredentials`. `AllowedHeaders` accepts `"*"` too, echoing the requested headers. `Vary: Origin`
is always set. Register it ahead of the HTTP handler so it sees every request first.

## See also

- [Getting started](getting-started.md) — builds a pipeline end to end and hosts it over `net/http`.
- [Result & status](message-result.md) — the `benzene.Result[T]` and status vocabulary middleware
  and handlers produce.
- [Core concepts §4](https://benzene.app/docs/specification/core-concepts.html) — the
  language-neutral pipeline model every port implements.
