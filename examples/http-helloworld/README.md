# http-helloworld

The greet handler hosted on a standalone `net/http` server via `httpbinding` - the Go counterpart of
the .NET repo's `examples/Asp` (hosting a Benzene service on the framework's own web server). The
focused "just host it on net/http" story: one route on a real `*http.Server`, wrapped in an ordinary
net/http middleware, with graceful shutdown.

## Run it

```
go run ./examples/http-helloworld
```

Listens on `:8080` (override with `PORT`).

## Try it

```
curl -X POST localhost:8080/greet -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST localhost:8080/greet -d '{"name":""}'
# 400 Bad Request
```

## What this demonstrates

- **Hosting on plain `net/http`**: `httpbinding.Handler` is an ordinary `http.Handler`, served by a
  real `*http.Server` - no framework, no code generation.
- **Composing with net/http middleware**: a request-counting middleware wraps the binding, proving
  it slots into whatever net/http middleware (logging, auth, metrics) you already use.
- **Graceful shutdown**: the server drains in-flight requests on `SIGINT`/`SIGTERM` via
  `http.Server.Shutdown`.
- **A port interface** (`Greeter`) resolved from the DI scope - swap the adapter without touching the
  handler.

For the binding's fuller surface - the wire envelope, health checks, both entry points - see
`examples/helloworld`.

## Module

`httpbinding` is `net/http`-native and dependency-free, so this example lives in the **root module**
(no `go.mod` of its own), like `examples/helloworld` and `examples/gcp-cloudrun-helloworld`. See
`main_test.go` for the `benzenetest`-driven component test.
