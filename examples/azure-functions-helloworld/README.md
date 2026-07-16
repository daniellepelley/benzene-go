# azure-functions-helloworld

The `helloworld` greet handler deployed as an Azure Functions [custom
handler](https://learn.microsoft.com/azure/azure-functions/functions-custom-handlers): Azure has
no native Go worker, so the function ships as a plain HTTP server (`main.go`) that the Functions
host forwards each invocation to.

## Layout

```
host.json               # customHandler config: run ./handler, don't forward raw HTTP
local.settings.json     # local `func start` settings (FUNCTIONS_WORKER_RUNTIME=custom)
Greet/function.json     # the "Greet" function: HTTP trigger, public route "greet"
main.go                 # the custom handler executable
```

`Greet/function.json`'s public `route` ("greet") is independent of the *local* invocation path
the host uses to call `main.go` (always `/Greet` - the function's folder name); see
`azurefunctions.Handler`'s doc comment for that distinction.

## Deploy

Requires the [Azure Functions Core
Tools](https://learn.microsoft.com/azure/azure-functions/functions-run-local) and an existing
Function App (`FUNCTIONS_WORKER_RUNTIME=custom`) created with the Azure CLI or Portal.

```
cd examples/azure-functions-helloworld
GOOS=linux GOARCH=amd64 go build -o handler .
func azure functionapp publish <your-function-app-name> --custom
```

`func ... --custom` zip-deploys this directory (host.json, local.settings.json is excluded,
`Greet/`, and the `handler` binary) as-is - no container required for this path.

### Container deployment

Azure Functions also supports [deploying as a Linux
container](https://learn.microsoft.com/azure/azure-functions/functions-how-to-custom-container)
for custom handlers with OS-level dependencies. This example doesn't include a Dockerfile for
that path: Microsoft's own base-image catalog for Functions containers changes over time (see
the [MCR catalog](https://mcr.microsoft.com/en-us/catalog?search=functions)), and this sandbox
had no way to verify a specific base image tag actually works end to end - shipping an
unverified Dockerfile for a deployment path is worse than not including one. If you need this
path, follow Microsoft's custom-container guide directly and copy `handler`, `host.json`,
`local.settings.json`, and `Greet/` into `/home/site/wwwroot` in your image.

## Try it

```
curl -X POST "https://<your-function-app-name>.azurewebsites.net/api/greet" -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST "https://<your-function-app-name>.azurewebsites.net/api/greet" -d '{"name":""}'
# 400 Bad Request
```

## The two custom-handler modes

`host.json` here sets `enableForwardingHttpRequest: false` (the default), so Azure forwards a
small JSON envelope (`Data`/`Metadata` in, `Outputs`/`ReturnValue` out) rather than the raw HTTP
request - that's what the `azurefunctions` package adapts. Setting it to `true` switches Azure
to forward the raw HTTP request/response instead; in that mode, skip `azurefunctions` entirely
and pass `httpbinding.Handler` straight to `http.ListenAndServe` (reading
`FUNCTIONS_CUSTOMHANDLER_PORT` instead of `PORT`) - functionally equivalent, one less package in
the dependency graph, at the cost of losing the envelope's structured `Data`/`Metadata` (route
params, trigger metadata) if you ever need them.

## What was verified in this sandbox

This sandbox has no `func` CLI and no Azure subscription, so nothing here was actually deployed.
What *was* verified locally:

- `go test ./examples/azure-functions-helloworld/...` - `newHandler` against the exact
  Data/Metadata JSON shape the Functions host sends, including the failure path (see
  `main_test.go`).
- `GOOS=linux GOARCH=amd64 go build -o handler .` compiles cleanly.
- `azurefunctions`'s own test suite (`azurefunctions/azurefunctions_test.go`) exercises the
  full custom-handler contract - matched/unmatched routes, malformed payloads, a missing `req`
  trigger - all asserting the outer HTTP 200 / `Outputs.res.statusCode` split the real Functions
  host relies on.
