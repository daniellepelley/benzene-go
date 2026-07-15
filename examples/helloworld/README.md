# helloworld

A minimal Benzene service showing the pieces fit together: a handler behind a port interface,
a health check, dependency injection, and both of `httpbinding`'s HTTP entry points.

## Run it

```
go run ./examples/helloworld
```

Listens on `:8080` (override with the `PORT` environment variable).

## Try it

```
# Native REST-style route, real HTTP status codes
curl -X POST localhost:8080/greet -d '{"name":"World"}'
# {"greeting":"Hello, World!","count":1}

curl -X POST localhost:8080/greet -d '{"name":""}'
# 400 Bad Request

# Health check (the reserved "healthcheck" topic)
curl localhost:8080/health
# {"isHealthy":true,"healthChecks":{"memory":{"status":"ok","type":"memory"}}}

# The raw wire-contracts.md envelope, for service-to-service calls with no route table
curl -X POST localhost:8080/invoke \
  -d '{"topic":"greet","headers":{},"body":"{\"name\":\"Envelope\"}"}'
# {"statusCode":"Ok","headers":{"content-type":"application/json"},"body":"{\"greeting\":\"Hello, Envelope!\",\"count\":2}"}
```

## What this demonstrates

- **A port interface** (`GreetingCounter`) with an in-memory adapter, registered as a singleton
  via `benzene.AddSingleton` - swap the adapter without touching the handler.
- **Dependency injection from a handler** via `benzene.ScopeFromContext(ctx)` +
  `benzene.GetService[T]`, since `Handler`'s signature doesn't carry a `*Scope` directly.
- **The three-phase app lifecycle** (`GetConfiguration` / `ConfigureServices` / `Configure`) via
  `benzene.App`.
- **Health check interception**: `healthcheck.Middleware` short-circuits the pipeline for the
  reserved `healthcheck` topic before the router ever sees it.
- **Both HTTP entry points**: `httpbinding.Handler` (an explicit route table, native HTTP status
  codes) and `httpbinding.EnvelopeHandler` (the wire envelope, always HTTP 200).

See `main_test.go` for an end-to-end test of all of the above over `httptest.NewServer`.
