# opentelemetry-helloworld

The greet handler wrapped in the `diagnostics` package's tracing middleware, so every invocation
emits an OpenTelemetry span - the Go counterpart of the .NET repo's `examples/OpenTelemetry`.

## Run it

```
go run ./examples/opentelemetry-helloworld
```

Listens on `:8080` (override with `PORT`). Every request prints its trace - a server span plus a
nested `Greeter.Greet` child span - to the console via a stdout exporter, so there's no collector to
stand up:

```
curl -X POST localhost:8080/greet -d '{"name":"World"}'
# {"greeting":"Hello, World!"}   ...and a pretty-printed trace on the server's stdout
```

## What this demonstrates

- **One span per invocation**: `diagnostics.Middleware`, registered outermost, starts a server span
  named by topic, joined to any inbound W3C `traceparent`, tagged `benzene.topic` / `benzene.status`
  (the Benzene status verbatim, not an HTTP code), and emits the `benzene.messages.processed` /
  `benzene.message.duration` metrics.
- **Nested spans**: the `Greeter` adapter starts its own `Greeter.Greet` child span off the
  span-carrying context threaded through the handler, so the exported trace shows the handler's work
  nested under the server span - the shape a real service's spans take.
- **App owns the SDK**: the middleware depends on the OpenTelemetry *API* only; `main()` installs the
  SDK `TracerProvider` (here a stdout exporter with an always-on sampler). Point the same wiring at
  an OTLP exporter to ship to Tempo/Jaeger/Datadog instead - nothing about the middleware changes.
  With no SDK installed the API's no-op defaults make the middleware free and silent.

## Module

This is its own Go module (`go.mod`) - it depends on the `diagnostics` module and the OpenTelemetry
SDK + stdout exporter, so it can't live in the zero-dependency root module. `go.work` ties it together
for local development. See `main_test.go` for the `benzenetest`-driven component test: it swaps the
stdout exporter for an in-memory one and asserts on the exact spans (server + nested child) the
pipeline produces - no collector needed.
