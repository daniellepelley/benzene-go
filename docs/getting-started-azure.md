# Getting Started on Azure Functions

This guide takes the `greet` handler from [Getting Started](getting-started.md) and deploys it to
[Azure Functions](https://learn.microsoft.com/azure/azure-functions/) as a **custom handler**. Read
[Getting Started](getting-started.md) first — this guide assumes you already know how a handler, the
three-phase `App`, and the `ApplicationBuilder` fit together, and only covers what changes when the
host is Azure Functions.

Azure has no native Go worker, so a Go function runs as an Azure Functions
[custom handler](https://learn.microsoft.com/azure/azure-functions/functions-custom-handlers): your
program is a plain HTTP server, and the Functions host forwards each trigger invocation to it over a
small JSON envelope (`Data`/`Metadata` in, `Outputs`/`ReturnValue` out). The `azurefunctions` package
adapts that envelope to a Benzene [pipeline](https://benzene.app/docs/specification/core-concepts),
so your handlers stay identical to the ones you'd run behind plain HTTP, AWS Lambda, or anywhere else.

The complete, runnable version of everything below is
[`examples/azure-functions-helloworld`](../examples/azure-functions-helloworld/).

## Prerequisites

- [Go](https://go.dev/dl/) (the version in the repo's `go.mod`)
- [Azure Functions Core Tools v4](https://learn.microsoft.com/azure/azure-functions/functions-run-local)
  — provides `func` for running locally and deploying
- An Azure subscription and the [Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli),
  if you want to deploy

## 1. The handler

Business logic lives in a handler, exactly as in [Getting Started](getting-started.md) — nothing
about it is Azure-specific, which is the point. It takes a request, returns a
[`Result[T]`](https://benzene.app/docs/specification/core-concepts), and knows nothing about the
transport that invoked it:

```go
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

`benzene.Ok` and `benzene.BadRequest` return the Benzene status the binding maps to a real HTTP
status code — `Ok` → 200, `BadRequest` → 400 — via the
[status mapping](https://benzene.app/docs/specification/wire-contracts) the `azurefunctions` binding
applies for you.

## 2. The app

The composition root is the ordinary three-phase `benzene.App` — the same shape every Benzene host
boots from. `ConfigureServices` registers the handler under a
[topic](https://benzene.app/docs/specification/core-concepts); `Configure` builds the pipeline with
`RouterMiddleware`, which dispatches each request to the handler registered for its topic:

```go
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
		},
	}
}
```

## 3. Wire up the custom handler

This is the only Azure-specific code. `azurefunctions.Handler` takes the built
`*benzene.ApplicationBuilder` and an HTTP route table, and returns an `http.Handler` that speaks the
custom-handler `Data`/`Metadata` envelope:

```go
func routes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/Greet", Topic: benzene.NewTopic("greet")}}
}

func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return azurefunctions.Handler(builder, routes())
}
```

The route table is the same `httpbinding.Route` type the plain-HTTP binding uses (method + path +
topic), so `azurefunctions.Handler` mirrors `httpbinding.Handler`'s shape — an explicit route table
and real HTTP status codes — rather than inventing a new contract.

**One subtlety worth internalizing:** `Route.Path` here is `/Greet`, the **local invocation path**
the Functions host uses to call your process — by default `/<FunctionName>`, the name of that
function's folder (see `Greet/function.json`). This is independent of the **public** `route`
(`"greet"`) declared in `function.json`, which is the URL your users hit. The host maps the public
route to the function, then calls your handler on the local path; `azurefunctions.Handler` routes on
that local path.

Path parameters captured by a `{param}` route template arrive on the request as `route-<name>` wire
headers, the same convention `httpbinding` uses.

## 4. `main`

`azurefunctions.ListenAddr()` reads the port the Functions host assigns via
`FUNCTIONS_CUSTOMHANDLER_PORT` (the custom-handler analogue of Cloud Run's `PORT`), defaulting to
`:8080` when you run the handler outside the host. `main` boots the app and serves the handler on it:

```go
func main() {
	handler := newHandler(newApp().Run())
	addr := azurefunctions.ListenAddr()
	log.Printf("azure-functions-helloworld listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
```

`newApp().Run()` runs the three phases (`GetConfiguration` → `ConfigureServices` → `Configure`) once
and returns the built `*benzene.ApplicationBuilder` — the same lifecycle described in
[Getting Started](getting-started.md), unchanged by the host.

## 5. The Functions host files

A custom handler needs three configuration files alongside your compiled binary.

`host.json` — tells the Functions host to run your binary and use the JSON-envelope mode (the mode
the `azurefunctions` package adapts):

```json
{
  "version": "2.0",
  "customHandler": {
    "description": {
      "defaultExecutablePath": "handler",
      "workingDirectory": "",
      "arguments": []
    },
    "enableForwardingHttpRequest": false
  }
}
```

`Greet/function.json` — the `Greet` function: an HTTP trigger bound to the input name `req` (the key
`azurefunctions.Handler` reads the trigger data from), with the public route `greet`:

```json
{
  "bindings": [
    {
      "authLevel": "anonymous",
      "type": "httpTrigger",
      "direction": "in",
      "name": "req",
      "methods": ["post"],
      "route": "greet"
    },
    {
      "type": "http",
      "direction": "out",
      "name": "res"
    }
  ]
}
```

`local.settings.json` — local `func start` settings; the key line is
`FUNCTIONS_WORKER_RUNTIME=custom`:

```json
{
  "IsEncrypted": false,
  "Values": {
    "AzureWebJobsStorage": "",
    "FUNCTIONS_WORKER_RUNTIME": "custom"
  }
}
```

## 6. Run it locally

Build the binary named in `host.json` (`handler`), then start the host:

```bash
cd examples/azure-functions-helloworld
go build -o handler .
func start
```

`func start` sets `FUNCTIONS_CUSTOMHANDLER_PORT`, launches your `handler` binary, and forwards
requests to it. Hit the public route (Azure prefixes HTTP routes with `/api` by default):

```bash
curl -X POST "http://localhost:7071/api/greet" -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST "http://localhost:7071/api/greet" -d '{"name":""}'
# 400 Bad Request
```

## 7. Test it — without the host

You don't need `func start` (or the network) to test the binding. The `benzenetest` package boots
your real app and pushes a native custom-handler invocation straight through `azurefunctions.Handler`,
reading the framework-mapped status back out of the `Outputs["res"]` envelope:

```go
func newTestHost() *benzenetest.Host {
	return benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))
}

func TestGreet(t *testing.T) {
	resp := benzenetest.SendAzureHTTP(t, newTestHost(), http.MethodPost, "/Greet", greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("res.StatusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	// resp.Body is {"greeting":"Hello, World!"}
}
```

`benzenetest.SendAzureHTTP` is the Azure analogue of `SendAPIGateway` — it builds the exact
`Data`/`Metadata` JSON the Functions host sends and asserts the outer HTTP 200 / `Outputs.res.statusCode`
split that the real host relies on (see [Supported triggers](#supported-triggers) for why that split
matters). `SendAzureQueue`, `SendCosmosChangeFeed`, `SendTimer`, and `SendEventGrid` do the same for
the other triggers.

Run the suite the usual way:

```bash
go test ./examples/azure-functions-helloworld/...
```

## 8. Deploy

Build a Linux binary (the Functions host runs Linux), then zip-deploy the directory with `func`:

```bash
cd examples/azure-functions-helloworld
GOOS=linux GOARCH=amd64 go build -o handler .
func azure functionapp publish <your-function-app-name> --custom
```

`func ... --custom` zip-deploys `host.json`, `Greet/`, and the `handler` binary as-is (it excludes
`local.settings.json`) — no container required. This assumes an existing Function App created with
`FUNCTIONS_WORKER_RUNTIME=custom`; create one with the Azure CLI or the Portal first.

Once deployed, hit the printed URL at `/api/greet`. Azure Functions also supports
[deploying a custom handler as a Linux container](https://learn.microsoft.com/azure/azure-functions/functions-how-to-custom-container)
if you have OS-level dependencies; the example's README explains why it doesn't ship a Dockerfile for
that path.

## The two custom-handler modes

`host.json` above sets `enableForwardingHttpRequest: false` (the default). In this mode Azure
forwards the structured `Data`/`Metadata` envelope, which is what `azurefunctions.Handler` adapts —
and what gives you access to trigger metadata and (for the non-HTTP triggers below) message
properties.

Setting it to `true` switches Azure to forward the **raw** HTTP request/response instead. In that
mode you skip the `azurefunctions` package entirely and pass `httpbinding.Handler` straight to
`http.ListenAndServe` (reading `FUNCTIONS_CUSTOMHANDLER_PORT` in place of `PORT`) — functionally
equivalent for a pure-HTTP function, one less package in the graph, at the cost of the envelope's
structured metadata. Raw forwarding only exists for HTTP triggers, so the queue and Cosmos triggers
below require the default (`false`) mode.

## Supported triggers

The `azurefunctions` package implements **six** trigger shapes today: HTTP, queue-shaped (Storage
Queue / Service Bus), Cosmos DB Change Feed, Timer, Event Grid, and Event Hubs. Each is a separate
`http.Handler` you mount on that function's local invocation path; a single custom handler can host
several at once by mounting them on an `http.ServeMux`, one per function path.

### HTTP — `azurefunctions.Handler`

Covered in full above. Topic-routed through an `httpbinding.Route` table, real HTTP status codes,
`route-<name>` headers for path parameters. This is the
[HTTP transport binding](https://benzene.app/docs/specification/transport-bindings) over the
custom-handler envelope.

The HTTP handler always answers the Functions host with an **outer** HTTP 200; the real result
travels inside `Outputs.res.statusCode`. A non-200 outer status would tell the host that the custom
handler *process* failed, not that your application returned an error — so an application-level 400 or
404 rides in the inner status, exactly as the host expects.

### Queue Storage & Service Bus — `azurefunctions.QueueHandler`

```go
mux.Handle("/GreetQueue", azurefunctions.QueueHandler(builder, "queueItem"))
```

`QueueHandler` adapts queue-shaped triggers — **Azure Storage Queue** and **Service Bus**
queue/topic — which share the same `Data`/`Metadata` invocation envelope. `dataName` is the trigger
binding's `name` from that function's `function.json` (e.g. `"queueItem"` for a Storage Queue,
`"mySbMsg"` for Service Bus); the message is read from `Data[dataName]`.

[Topic resolution](https://benzene.app/docs/specification/wire-contracts) follows the same order as
the AWS SQS/SNS bindings:

1. a `topic` entry in `Metadata.UserProperties` (Service Bus application properties — the native
   per-message attribute channel; the remaining string properties become wire headers), else
2. the message body parsed as a full `wire.Request` envelope (the only option on Storage Queues,
   which carry no per-message attributes), else
3. an empty topic, which `RouterMiddleware` maps to a validation error — the message is **failed**,
   never silently dropped.

Unlike the HTTP handler, a **non-success dispatch answers the host with outer HTTP 500**. On a
queue-shaped trigger, a non-2xx custom-handler response is how the invocation is marked failed, which
hands the message to the platform's own retry machinery — Storage Queue redelivery up to
`maxDequeueCount` then the poison queue; Service Bus abandon/redelivery then the dead-letter queue.
This is the Azure counterpart of the AWS bindings' returned error / batch-item-failure.

### Cosmos DB Change Feed — `azurefunctions.CosmosHandler`

```go
mux.Handle("/OrdersChanged", azurefunctions.CosmosHandler(builder, benzene.NewTopic("orders:changed"), "documents"))
```

`CosmosHandler` adapts the Cosmos DB Change Feed trigger. The Functions host owns the change-feed
connection and lease container and forwards each delivered batch of changed documents under
`Data[dataName]` (a JSON array), so your handler never opens a Cosmos connection itself.

This binding is **fan-in, not topic-routed**: the whole batch of changed documents is *one* pipeline
invocation — not one per document — dispatched to the single topic you name in the call. The handler
receives the batch as its request, idiomatically a slice: `benzene.Handler[[]OrderDocument, TRes]`.

Checkpointing is batch-level (the change feed has no per-document resume token) and happens on a
successful return only, so `CosmosHandler` uses the same outer-status convention as `QueueHandler`: a
successful dispatch answers outer HTTP 200 (the host advances the lease past this batch) and any
non-success dispatch answers outer HTTP 500 (the host does not checkpoint and redelivers the whole
batch). Design the handler to be **idempotent** across a redelivered batch.

### Timer — `azurefunctions.TimerHandler`

```go
mux.Handle("/NightlyCleanup", azurefunctions.TimerHandler(builder, benzene.NewTopic("nightly-cleanup"), "myTimer"))
```

A scheduled tick carries no message, so — like the Cosmos trigger — this is **fan-in, not
topic-routed**: the topic is the scheduled job's identity, named in code, and the body is the tick's
schedule info (`IsPastDue`, `ScheduleStatus`). A handler with an empty request type (`struct{}`)
binds cleanly if it doesn't care. A timer has no redelivery, so the outer 200/500 only surfaces a
failed run to the host's monitoring — it does not cause a retry. Test with `benzenetest.SendTimer`.

### Event Grid — `azurefunctions.EventGridHandler`

```go
mux.Handle("/BlobCreated", azurefunctions.EventGridHandler(builder, "eventGridEvent"))
```

One event per invocation (the host de-batches an Event Grid delivery). The topic is the event
**type** — Event Grid schema's `eventType` or CloudEvents 1.0's `type` (told apart by
`specversion`) — so `benzene.NewTopic("Microsoft.Storage.BlobCreated")` handles that event. The body
is the event's `data`; the envelope's `id`, `subject`, and `source` become headers. A non-success
dispatch answers outer HTTP 500 so Event Grid's own retry and dead-letter machinery takes over —
the same fire-and-forget posture as `QueueHandler`. Test with `benzenetest.SendEventGrid`.

### Event Hubs — `azurefunctions.EventHubHandler`

```go
mux.Handle("/OrderPlaced", azurefunctions.EventHubHandler(builder, "eventHubMessages"))
```

Requires the trigger's `function.json` to set `"cardinality": "many"` (batch mode — the shape the
.NET binding also uses). Each event in the batch is its own pipeline invocation, topic-resolved
exactly like a Service Bus message on `QueueHandler` (a `topic` application property, else the body
as a wire envelope). Events dispatch strictly in order and processing **stops at the first
failure**, answering outer HTTP 500 so the host redelivers the whole batch — checkpointing is
invocation-level, so handlers must be idempotent across a redelivered batch.

### Other triggers

Blob Storage (and the other SDK-typed triggers) are **not** implemented in the Go port. They follow
the same `Data`/`Metadata` envelope, so a new adapter is the `QueueHandler` pattern with a different
payload interpretation; nothing in the design blocks them, but the package does not ship them today
(see [`ROADMAP.md`](../ROADMAP.md)).

For **self-hosted** compute (a container or VM that owns its own receive loop, rather than the
Functions host), the `azureservicebus`, `azureeventhub`, and `azurecosmos` modules provide worker
counterparts to these triggers.

## See also

- [Getting Started](getting-started.md) — the handler, `App`, and pipeline this guide builds on
- [`examples/azure-functions-helloworld`](../examples/azure-functions-helloworld/) — the complete,
  runnable example, including the CI deploy workflow
- [Core concepts](https://benzene.app/docs/specification/core-concepts) — topics, results, pipeline,
  and the fan-in shape the Cosmos trigger uses
- [Wire contracts](https://benzene.app/docs/specification/wire-contracts) — topic resolution and the
  status vocabulary the bindings map
- [Transport bindings](https://benzene.app/docs/specification/transport-bindings) — the HTTP binding
  the custom-handler HTTP adapter mirrors
