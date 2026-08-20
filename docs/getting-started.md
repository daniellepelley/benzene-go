# Getting started

Build a small Benzene service in Go — from an empty folder to a running HTTP endpoint you can
`curl` — and see how a handler that knows nothing about the transport gets hosted over plain
`net/http`.

This guide walks through the same code as the runnable
[`examples/helloworld`](https://github.com/daniellepelley/benzene-go/tree/main/examples/helloworld)
example. If you'd rather read the finished program, start there; if you'd rather build it up a piece
at a time, read on.

## The one idea

Benzene's promise is **write your message handler once, host it anywhere**. Everything below is one
shape:

1. **A handler** — your logic: a plain function that takes a typed request and returns a typed
   `benzene.Result[T]`. A result carries a Benzene **status** (`ok`, `not-found`,
   `validation-error`, …), an optional payload, and any error messages — success and failure are
   both ordinary return values, never a thrown error. The handler itself never imports `net/http`
   and never names an HTTP code; whichever transport hosts it translates the status into its own
   native signal.
2. **A topic** — a stable string (`"greet"`) that names what the handler serves. Every transport —
   an HTTP route, a queue message, a service-to-service call — resolves to a topic, and the
   registry binds each topic to exactly one handler.
3. **A pipeline** — an ordered onion of middleware (logging, health checks, your own), ending in
   the router that dispatches a topic to its handler. Cross-cutting concerns live here, not in the
   handler.
4. **A transport binding** — the *only* platform-specific part. Here it's `httpbinding` over
   `net/http`; on a cloud host it's a Lambda or Azure Functions binding, and the handler is
   byte-for-byte identical.

These four concepts are language-neutral — every Benzene port implements the same model, defined
once on the website. You don't need the spec to follow this guide; when you want the deeper
reference, [Core concepts](https://benzene.app/docs/specification/core-concepts.html) has the full
model and [Wire contracts](https://benzene.app/docs/specification/wire-contracts.html) the envelope
and status vocabulary.

## Prerequisites

- **Go 1.24 or newer** (this module targets `go 1.24`).

## 1. Set up a project

```bash
mkdir hello-benzene && cd hello-benzene
go mod init example.com/hello-benzene
go get github.com/daniellepelley/benzene-go
```

Everything in this guide lives in the root `benzene` package plus the `httpbinding` and `healthcheck`
subpackages — all part of the one module you just added, no extra third-party dependencies.

## 2. Write a handler

A Benzene handler is a plain function from a request to a `benzene.Result[T]`. It's a
`benzene.Handler[TReq, TRes]` — a `func(context.Context, TReq) benzene.Result[TRes]`. Create
`main.go`:

```go
package main

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}
```

The request and response are ordinary structs with JSON tags — the binding decodes the request body
into `greetRequest` and marshals the response payload back out. The handler returns *values*, not
errors: `benzene.Ok(...)` for success and `benzene.BadRequest[greetResponse]("...")` for a
client error. There's a constructor for each status in the framework vocabulary —
`benzene.Ok`, `benzene.Created`, `benzene.NotFound`, `benzene.Conflict`,
`benzene.ValidationError`, and so on — each mapping to a Benzene
[status](https://benzene.app/docs/specification/wire-contracts.html) the transport translates into
its own native failure signal (an HTTP code, here).

## 3. Compose the app

The composition root wires three things together: the **registry** (topic → handler), the
**container** (dependency injection), and the **pipeline** (middleware, ending in the router). The
`benzene.App` type runs these as a three-phase lifecycle — `GetConfiguration`, then
`ConfigureServices`, then `Configure` — so the exact wiring that ships is the wiring your tests boot
from.

Add to `main.go`:

```go
import (
	// ...existing imports...
	"github.com/daniellepelley/benzene-go/healthcheck"
)

func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			checks := []healthcheck.Check{
				healthcheck.NamedCheck("memory", func(context.Context) healthcheck.CheckResult {
					return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "memory"}
				}),
			}
			builder.UsePipeline(benzene.NewPipeline(
				healthcheck.Middleware(checks),
				benzene.RouterMiddleware(builder.Registry),
			))
		},
	}
}
```

Three things to notice:

- **`benzene.MustRegister`** binds the `"greet"` topic to the handler. Registration is explicit —
  there's no reflection-based scanning; the registry is the complete, authoritative list of what this
  service serves. The explicit form it composes is **`benzene.Register`**, which *returns* an error
  when a topic is registered twice:

  ```go
  benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
  ```

  `MustRegister` panics with that same error instead. Both run in `ConfigureServices`, at start-up,
  before any message is handled — so a duplicate topic fails at boot naming the topic, never on the
  message path. Use `Register` when the caller has somewhere better to send the error than a panic.
  The type parameters are inferred from the handler's signature in either form; a
  `benzene.Handler[greetRequest, greetResponse](...)` conversion is never required for a function
  that already has the handler shape.
- **The pipeline order matters.** `healthcheck.Middleware` runs first and short-circuits the reserved
  health-check topic before the router ever sees it; `benzene.RouterMiddleware` is the terminal
  middleware that resolves the topic and dispatches to the handler, so it's registered last (see
  [Core concepts §4](https://benzene.app/docs/specification/core-concepts.html) on the pipeline).

`TConfig` is `struct{}` here because this service has no configuration; a real service would make it
a config struct returned by `GetConfiguration`. All three phases are optional — this service leaves
`GetConfiguration` out entirely, and a service whose pipeline is *just* the router can leave
`Configure` out too, because `App.Run` installs
`benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry))` when `Configure` set none. Write
that line yourself — or `builder.UseDefaultPipeline()`, the one-line form of it — whenever you want
the default stated rather than implied. `ConfigureServices` is also where you'd register
dependencies against the `*benzene.Container` (`benzene.AddSingleton`, `benzene.AddScoped`,
`benzene.AddTransient`) and resolve them inside a handler with `benzene.ScopeFromContext(ctx)` +
`benzene.GetService[T]` — the [helloworld example](https://github.com/daniellepelley/benzene-go/tree/main/examples/helloworld)
does exactly that with a shared counter.

## 4. Define the route table

`httpbinding` needs a table mapping each HTTP `(method, path)` to a topic. Keep it in one function so
the same table drives both `main` and your tests:

```go
import (
	// ...existing imports...
	"net/http"

	"github.com/daniellepelley/benzene-go/httpbinding"
)

func routes() []httpbinding.Route {
	return []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	}
}
```

A `Path` can contain `{name}` segments to capture path parameters — each captured segment arrives at
the handler as a `route-<name>` wire header. `httpbinding.HealthPath` (`/benzene/health`) is the
well-known mount the default service standard reserves for the health check, so it reads as framework
infrastructure rather than a domain endpoint.

## 5. Serve it over HTTP

The transport binding is the last piece. `httpbinding.Handler` turns the builder and the route table
into an ordinary `http.Handler`, so you serve it with the standard library — nothing Benzene-specific
about running the server:

```go
func main() {
	builder := newApp().Run()

	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	mux.Handle("/", httpbinding.Handler(builder, routes()))

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
```

`newApp().Run()` executes the three-phase lifecycle once and hands back the built
`*benzene.ApplicationBuilder`. Two entry points are mounted:

- **`httpbinding.Handler`** — the native REST-style front door. It maps a matched route to a topic,
  dispatches through the pipeline, and maps the result back to a real HTTP status code and JSON body.
- **`httpbinding.EnvelopeHandler`** at `httpbinding.EnvelopePath` (`/benzene/invoke`) — the raw wire
  envelope carried over HTTP (always HTTP 200; the real outcome is inside the envelope's own
  `statusCode`). It's for service-to-service calls where there's no route table to agree on. It's
  optional — drop it if you don't need it.

## 6. Run it

```bash
go run .
# listening on :8080
```

In another terminal:

```bash
# Native REST-style route, real HTTP status codes
curl -X POST localhost:8080/greet -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

# A missing name is a validation failure -> HTTP 400
curl -i -X POST localhost:8080/greet -d '{"name":""}'
# HTTP/1.1 400 Bad Request

# The reserved health check
curl localhost:8080/benzene/health
# {"isHealthy":true,"healthChecks":{"memory":{"status":"ok","type":"memory"}}}

# The raw wire envelope, for service-to-service calls with no route table
curl -X POST localhost:8080/benzene/invoke \
  -d '{"topic":"greet","headers":{},"body":"{\"name\":\"Envelope\"}"}'
# {"statusCode":"ok","headers":{"content-type":"application/json"},"body":"{\"greeting\":\"Hello, Envelope!\"}"}
```

The handler returned `benzene.Ok(...)` and `benzene.BadRequest(...)`; the binding mapped those Benzene
statuses to HTTP `200` and `400` via the
[wire-contracts status table](https://benzene.app/docs/specification/wire-contracts.html). The
handler never named an HTTP code.

## 7. Test it without a real server

Because the app boots from one composition root (`newApp`), a test exercises exactly the wiring that
ships. The `benzenetest` package runs the same lifecycle in-process and pushes native events in the
front door — no `net/http` listener required:

```go
package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/daniellepelley/benzene-go/benzenetest"
)

func TestGreet(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}

	var got greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", got.Greeting, "Hello, World!")
	}
}
```

`benzenetest.SendHTTP` drives the native-HTTP front door; `benzenetest.SendEnvelope` drives the wire
envelope. To test these same handlers on a cloud host later, only the `Send*` call changes — the
host setup and assertions stay identical. The example's
[`main_test.go`](https://github.com/daniellepelley/benzene-go/blob/main/examples/helloworld/main_test.go)
covers the greet endpoint, the health check, the envelope round-trip, and a 404.

## What just happened

You wrote a handler that knows nothing about HTTP, bound it to a topic, ran it through a middleware
pipeline, and hosted it over `net/http` with `httpbinding` — the four pieces from
[The one idea](#the-one-idea). The handler code never mentions the transport, which is the whole
point of Benzene's ports-and-adapters design: **the handler is the asset; the host is a detail.**

That detail is the only thing that changes when you deploy to a cloud provider.

## Why not just net/http?

Worth asking honestly: `http.HandleFunc("/greet", func(w http.ResponseWriter, r *http.Request) {
...})` does the same job as this guide's seven steps in a handful of lines, no `httpbinding` import.
For an HTTP-only service that never talks to anything else, that's a fair trade — the stdlib (or
chi/gorilla if you want a router) already gives HTTP its own routing, and you don't need Benzene to
get it.

The payoff shows up the moment this same handler needs a **second** entry point — a queue another
team publishes to, a Kafka topic, a batch job that used to call this endpoint but really just wants
to drop a message. A bare `http.HandlerFunc` has no answer for that; you'd write a second, separate
handler and keep both in sync by hand. With Benzene the handler above doesn't change at all: the
self-hosted `awssqs.Consumer` or `kafka.Consumer` point a worker at the *same* `greetHandler`,
because it was never written against `http.ResponseWriter` in the first place — see
[Getting Started: Kubernetes](getting-started-kubernetes.md) for that running as three goroutines in
one binary, one Deployment. If HTTP genuinely is and always will be the only way in, reach for
`net/http` (or chi/gorilla) directly instead — you'll write less code, not more.

## Next: host it in the cloud

The same `newApp()` composition root and the same handler run behind a different transport binding on
each cloud host. Each guide starts from this service and swaps only the host wiring:

- [Getting started: AWS Lambda](getting-started-aws.md) — one function behind API Gateway, SQS, SNS,
  and more.
- [Getting started: Azure Functions](getting-started-azure.md) — the custom-handler binding for HTTP,
  queue, and event triggers.
- [Getting started: Google Cloud](getting-started-google.md) — Cloud Run (plain `net/http`, no new
  package) and Pub/Sub push subscriptions.
- [Getting started: Kubernetes](getting-started-kubernetes.md) — one handler hosted over HTTP, SQS,
  and Kafka from a single binary and Deployment.
- [gRPC](getting-started-grpc.md) and [Kafka](getting-started-kafka.md) — the unary gRPC binding and
  the self-hosted Kafka consumer/producer.

For the full set of runnable services — local HTTP, every cloud host, and the mesh demo — see the
[`examples/`](https://github.com/daniellepelley/benzene-go/tree/main/examples) directory.
