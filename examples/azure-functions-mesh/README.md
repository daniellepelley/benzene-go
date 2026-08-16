# Azure Functions mesh — six chained Cloud Service Azure Functions + a push-based mesh

The Azure-deployable "mesh" estate for benzene-go: six domain Azure Functions (orders, payments,
shipping, inventory, notifications, analytics) chained over Service Bus/Event Hub/Event Grid, plus
a seventh **mesh** Function that ingests their register/heartbeat/trace/issue reports and serves
the fleet UI. It is the Go counterpart of
[benzene-dotnet's `examples/AzureFunctionsMesh`](https://github.com/daniellepelley/benzene-dotnet/tree/main/examples/AzureFunctionsMesh)
— same topology, same estate, **two deliberate divergences** (read the next two sections first).
It sits alongside this repo's other two mesh examples this session —
[`examples/k8s-mesh-helloworld`](../k8s-mesh-helloworld) (the push-based collector pattern on
Kubernetes) and [`examples/aws-lambda-mesh`](../aws-lambda-mesh) (the same pattern over AWS
Lambda Invoke) — proving the identical mesh push mechanism a third time, now hosted on Azure
Functions' custom-handler model.

## Divergence from .NET: push-only, no ARM discovery, plain HTTP push

benzene-dotnet's `examples/AzureFunctionsMesh/Mesh` **discovers** the six service Function Apps
by listing `Microsoft.Web/sites` through Azure Resource Manager (a system-assigned managed
identity with `Reader` on the resource group), then **interrogates** each one over HTTPS
(`GET /benzene/spec`, `GET /benzene/health`) on a **timer trigger**, writing the discovered
registry plus the aggregated catalog to **Blob Storage** (`Storage Blob Data Contributor`) — a
*pull* model needing real IAM.

**benzene-go has neither piece.** There is no Azure Resource Manager discovery provider anywhere
in this port, and no Blob-backed mesh artifact/catalog store — building either from scratch would
mean inventing a whole new discovery-source-plus-catalog-writer subsystem the `mesh`/`meshd`
packages don't have any abstraction for today, out of proportion for one example (the same
reasoning `examples/k8s-mesh-helloworld` and `examples/aws-lambda-mesh` already apply to their own
Kubernetes-API and AWS-Lambda-discovery equivalents). What this port has instead, fully built and
platform-agnostic, is [`meshd`](../../meshd) — a **push**-based collector: services announce
themselves and heartbeat/trace/report-issues to it over the wire envelope
(`benzene:mesh:register`/`:heartbeat`/`:traces`/`:issues`), exactly the mechanism both sibling
examples already prove end to end.

So this example's mesh Function is **push-only**, and Azure Functions' custom-handler model shapes
two concrete implementation choices:

1. **Plain HTTP push, via `httpclient.Client` as `mesh.Sender`.** Each of the six service
   Functions pushes register/heartbeat/trace/issue reports **directly to the mesh Function's
   `POST /benzene/invoke`** — an ordinary HTTP call, using
   [`httpclient.Client`](../../httpclient) as the underlying sender. It already satisfies
   `mesh.Sender`'s `Send(ctx, topic, headers, message) Result[json.RawMessage]` signature with
   **zero adapter code** needed — the same reuse `examples/k8s-mesh-helloworld/cmd/service`
   already proves for its own service-to-mesh push, over the exact same transport (this example
   just swaps a direct Lambda Invoke — `examples/aws-lambda-mesh`'s own choice — back to plain
   HTTP, since Azure Functions has no Lambda-Invoke-shaped equivalent and HTTP is its native
   trigger). [`meshapp.App`](meshapp/meshapp.go) wires `mesh.PushExporter`/`mesh.PushIssueExporter`
   around that client exactly the way `examples/k8s-mesh-helloworld/cmd/service` does.
2. **No per-invocation pipeline rebuild, unlike `examples/aws-lambda-mesh`.** An Azure Functions
   custom handler is a **persistent process** — the host starts the handler executable once and
   keeps it running as an ordinary HTTP server for the life of a warm instance (Consumption cold
   starts aside), unlike a Lambda execution environment, which **freezes** between invocations.
   So `meshapp.App` builds its trace/issue exporter pair **once**, at startup, and lets their own
   background goroutines batch and flush on their normal timers — no `examples/aws-lambda-mesh`-
   style `BatchSize: 1`/rebuild-every-invocation workaround needed. See
   [`meshapp/meshapp.go`](meshapp/meshapp.go)'s package doc for the full reasoning.

Net effect: you get a fully live mesh — real registered services, real health, real trace flows
from the orders → payments → shipping chain and every fan-out below — deployed as plain Azure
Functions with **no Azure Resource Manager discovery role, no managed identity, and no Blob
catalog container** anywhere in the stack (materially simpler than .NET's pull-based IAM story —
see [`deploy/main.tf`](deploy/main.tf)'s own header comment). What you *don't* get is a catalog of
services that exist but have never pushed a report (a deployed-but-never-invoked Function is
invisible here; in .NET it would still show up, discovered-but-uninterrogated) — the same fair
trade both sibling mesh examples already make.

## The one legitimate framework addition: `azurefunctions.EventHubHandler`

`azurefunctions.QueueHandler` (Service Bus) and `azurefunctions.EventGridHandler` already existed
in this port; there was no Azure-Functions-trigger adapter for Event Hub (only a self-hosted
`azureeventhub.Consumer`). .NET hit this exact same gap building its own
`examples/AzureFunctionsMesh` — its own README says: *"The Event Hub egress↔Functions-trigger
round-trip needed a small framework addition... see `Benzene.Azure.Function.EventHub`."* So this
was expected, proportionate, in-pattern work, added to [`azurefunctions/`](../../azurefunctions)
(`eventhub.go` + `eventhub_test.go`), matching `QueueHandler`/`EventGridHandler`'s shape closely:

- **Batch mode only** (`"cardinality": "many"` in `function.json`), matching .NET's own
  `EventHubApplication`, which is built exclusively around `EventData[]` batches.
- **One pipeline invocation per event, not one per batch** — unlike the genuinely fan-in
  `CosmosHandler`/`TimerHandler`, an Event Hub batch is a batch of independently topic-routed
  Benzene messages (each event carries its own topic in its application properties, the same way
  a Service Bus message does), so each event in the batch gets its own `envelope.DispatchResult`
  call, with `QueueHandler`'s exact topic/headers/body resolution precedence reused per event.
- **Ordered, stop-at-first-failure dispatch.** Event Hubs delivers a partition's events in order,
  and the Functions host checkpoints a batch trigger at the *invocation* level — there is no way
  to acknowledge part of a batch, so any failure anywhere in it redelivers the whole thing
  regardless of how the handler processes the rest. `EventHubHandler` dispatches strictly in
  order and stops at the first non-success result, rather than running the remaining events
  anyway — the same ordered-stream stop-at-first-failure stance `azureeventhub.Consumer`,
  `awsdynamodb`, and `awskinesis` already take in this port (a deliberate divergence from .NET's
  `EventHubApplication`, which fans every event out concurrently and only surfaces failure once
  every event has run — see `azurefunctions/eventhub.go`'s doc comment for the full reasoning).

This is the **only** change made to `azurefunctions/` (or any other framework package) for this
example — `git diff --stat main -- azurefunctions/ azureservicebus/ azureeventhub/ azureeventgrid/
mesh/ meshd/ httpclient/ httpbinding/` shows exactly that.

### Two small pieces of glue that stay example-local, not framework code

`azurefunctions.Handler`'s Route table maps one *fixed* topic per local Function-folder path (see
its own doc comment on the local-invocation-path/public-route distinction) — which fits
`/benzene/spec` and `/benzene/health` perfectly (both are reserved topics intercepted by
middleware, `mesh.TopicID`/`healthcheck.ReservedTopic`) but cannot express **`POST
/benzene/invoke`**, whose topic travels in the request *body*, not a fixed route. Nor can it serve
the mesh Function's Fleet View (`meshd.Collector.ViewHandler`), which expects a real
`*http.Request`/`http.ResponseWriter`, not topic-routed dispatch. Both needs are met by two small
adapters in [`meshapp/httpadapters.go`](meshapp/httpadapters.go) — the same move
`examples/aws-lambda-mesh/cmd/mesh/main.go`'s own `dispatchEnvelopeOverHTTP` already makes for the
equivalent Lambda HTTP event shape. They stay **example-local** (not a package addition) because
they are pure custom-handler-protocol reimplementation, no new framework capability.

## The estate

```
  orders ──payment:take (Service Bus)──▶ payments ──shipment:book (Service Bus)──▶ shipping
    │                                       │                                          │
    └─order:placed (Event Hub, 2 consumer   ├─payment:captured (Event Grid)─▶ notifications,
      groups)─▶ inventory, notifications    │                                  analytics
                                             └─ shipment:dispatched (Event Grid) ─▶ inventory,
                                                                        notifications, analytics

  Every one of the six Functions above ALSO pushes register/heartbeat/traces/issues directly to:

  orders, payments, shipping, inventory, notifications, analytics
      │  (plain HTTP POST /benzene/invoke, httpclient.Client)
      ▼
    mesh   ── meshd.Collector (in-memory) ──▶ GET /benzene/fleet-ui
```

Each service is **one Azure Function App**, custom-handler model (matching
`examples/azure-functions-helloworld`'s hosting via `azurefunctions.Handler`, non-forwarding
mode — `host.json`'s `enableForwardingHttpRequest: false`), with `"extensions": {"http":
{"routePrefix": ""}}` so `/benzene/*` sits at the root. Because a non-forwarding custom handler
always invokes the SAME local path per Function folder (see `azurefunctions.Handler`'s doc
comment), each distinct HTTP entry point — Spec, Health, Invoke, and any native route — is its own
Function folder, all sharing one `http.ServeMux` in `main.go`:

| Service | Triggers (Function folders) | Registers | Publishes |
|---|---|---|---|
| `orders` | HTTP: `Spec`, `Health`, `Invoke`, `Orders` (`POST /orders`) | `order:create` | `payment:take` (Service Bus), `order:placed` (Event Hub) |
| `payments` | HTTP: `Spec`, `Health`, `Invoke`; Service Bus: `PaymentTake` | `payment:take` | `shipment:book` (Service Bus), `payment:captured` (Event Grid) |
| `shipping` | HTTP: `Spec`, `Health`, `Invoke`; Service Bus: `ShipmentBook` | `shipment:book` | `shipment:dispatched` (Event Grid) — terminal, no further hop |
| `inventory` | HTTP: `Spec`, `Health`, `Invoke`; Event Hub: `OrderPlaced` (consumer group `inventory`); Event Grid: `ShipmentDispatched` | `order:placed`, `shipment:dispatched` | — (pure consumer) |
| `notifications` | HTTP: `Spec`, `Health`, `Invoke`; Event Hub: `OrderPlaced` (consumer group `notifications`); Event Grid: `IntegrationEvents` (both event types) | `order:placed`, `payment:captured`, `shipment:dispatched` | — (pure consumer) |
| `analytics` | HTTP: `Spec`, `Health`, `Invoke`; Event Grid: `IntegrationEvents` (both event types) | `payment:captured`, `shipment:dispatched` | — (pure consumer) |
| `mesh` | HTTP only: `FleetUi`, `Invoke`, `Discovered` | `benzene:mesh:*` (via `meshd.Collector`) | — |

Both columns above are **hard-coded contract, never inferred**. *Registers* is each service's
`benzene.Register` calls (what it receives); *Publishes* is its `mesh.RegisterOutbound` records
(what it sends, `mesh.md` §2.3), declared for the whole estate in one switch —
[`domain.RegisterOutbound`](domain/domain.go), called from every `cmd/<service>/main.go` beside
that service's handler registration. The two together are the *only* source of the mesh's
producer/consumer graph: the collector draws a topic's **providers** from the descriptors that
registered a handler for it and its **consumers** from the descriptors that declared they send it,
before a single message flows (`mesh.md` §4). Trace propagation
(`mesh.WithTraceContext`, wired on every outbound client here) only marks those declared edges as
*observed* (§4.2) — it never adds one. Skip the outbound declaration and the catalog comes out
half-drawn: every topic with providers, no consumers, and nothing in the UI to say why.

Domain logic is deliberately trivial (see [`domain/domain.go`](domain/domain.go)) — the point of
this example is proving the mesh's transport wiring, not rich business behaviour, matching every
sibling mesh example's own stance.

## Projects

| Path | What it is |
|---|---|
| `domain/` | the trivial handlers for all six services — the Go counterpart of benzene-dotnet's `*/Domain.cs`, using this topology's own topic names (`payment:take`/`shipment:book`, matching `examples/k8s-mesh-helloworld`'s naming; `order:placed`/`payment:captured`/`shipment:dispatched`, matching `examples/aws-lambda-mesh`'s naming) |
| `meshapp/` | the shared composition root every one of the six service Functions is built from: registry/descriptor/Cloud-Service-Profile-route wiring, push fleet reporting (`App`, `meshapp.go`), and the two custom-handler HTTP adapters the mesh's own `/benzene/invoke` and Fleet View need (`httpadapters.go`) |
| `cmd/orders/`, `cmd/payments/`, `cmd/shipping/`, `cmd/inventory/`, `cmd/notifications/`, `cmd/analytics/` | one Function App per service: `main.go` + `host.json` + `local.settings.json` + one folder per Function (matching `examples/azure-functions-helloworld`'s layout) |
| `cmd/mesh/` | the mesh Function: wraps `meshd.Collector` directly (no `meshapp.App` — it's a thin collector wrapper, not a composite service, matching `examples/k8s-mesh-helloworld`'s and `examples/aws-lambda-mesh`'s own mesh binaries) |
| `deploy/` | Terraform for the whole estate (storage, Consumption plan, 7 Function Apps, Service Bus, Event Hub, Event Grid) + `build.sh` to cross-compile and zip the seven custom handlers |
| `../../.github/workflows/mesh-example-azure-functions-deploy.yml` | build zips → terraform apply → publish → wire Event Grid subscriptions → kick off the cascade → assert 6 discovered |
| `../../.github/workflows/mesh-example-azure-functions-destroy.yml` | terraform destroy |

This is its own Go module (see `go.mod`) — like `examples/aws-lambda-mesh`, it depends on the root
module plus the `azureservicebus`, `azureeventhub`, and `azureeventgrid` modules, so putting it
inside any one of those would create a dependency cycle.

## Deploy to Azure

Requires Go, [Terraform](https://developer.hashicorp.com/terraform/install) ≥ 1.5, and the
[Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli) logged in
(`az login`) with rights to create the resource group, storage, App Service plan, Function Apps,
and the Service Bus/Event Hub/Event Grid resources.

```bash
cd examples/azure-functions-mesh
deploy/build.sh                 # cross-compiles + zips all 7 handlers into deploy/build/*.zip
cd deploy
az group create -n benzene-go-fnmesh-rg -l westeurope
terraform init                  # local state by default; add -backend-config=... for azurerm remote state (see the workflow)
terraform apply -var resource_group=benzene-go-fnmesh-rg
# publish each Function App's zip (az functionapp deployment source config-zip), then:
terraform apply -var resource_group=benzene-go-fnmesh-rg -var wire_eventgrid_subscriptions=true
```

### From GitHub Actions

**Actions → Mesh Example Azure Functions Deploy → Run workflow.** Requires repo secret
`AZURE_CREDENTIALS` (a service principal JSON, e.g. from
`az ad sp create-for-rbac --sdk-auth --role contributor --scopes /subscriptions/<id>`) — this
repo's existing convention (see `deploy-azure-functions-helloworld.yml`,
`mesh-example-aws-lambda-deploy.yml`), rather than benzene-dotnet's GitHub-Environment setup. It
builds the seven zips, keeps Terraform state in a per-subscription storage account
(`<storage_account>tfstate`, created on first run), applies the stack, publishes each Function App,
wires the Event Grid subscriptions (a second apply, after warming the consumer apps), `POST`s one
order to kick off the cascade, and polls `GET /mesh/discovered` until it reads
`{"discovered":6}` — a real end-to-end proof of the push-based fleet story.
**Actions → Mesh Example Azure Functions Destroy → Run workflow** tears it back down (optionally
also deleting the resource group for a full cleanup).

## Try it

```bash
# 1. Kick off the cascade
curl -X POST "https://<project>-orders.azurewebsites.net/orders" \
  -H 'content-type: application/json' -d '{"customerId":"cust-1","sku":"espresso","quantity":2}'
# => {"orderId":"order-1","status":"created"}

# 2. Watch the estate register itself (populated within a couple of invocations)
curl "https://<project>-mesh.azurewebsites.net/mesh/discovered"
# => {"discovered":6}

# 3. Open the Fleet View in a browser
open "https://<project>-mesh.azurewebsites.net/benzene/fleet-ui"
```

The cascade (`orders → payments → shipping` over Service Bus, plus the Event Hub + Event Grid
fan-outs) runs asynchronously across the real Function Apps; each app's log stream shows it being
reached, and the Fleet View's flow explorer shows the trace once each hop has pushed its trace
event.

## Known first-deploy iteration points

- **Cold-start announce.** `meshapp.App.RunHeartbeatLoop` retries with a 2s backoff (30 attempts)
  before giving up on the *initial* register — a Function App that has never been invoked has
  never had the chance to receive an HTTP request at all (Consumption cold start), let alone
  announce; invoke it once (any of its routes) to bring it into the fleet, exactly like every
  other push-based mesh example's own iteration point.
- **Event Grid subscription validation needs a warm, published app** — the same first-deploy
  quirk benzene-dotnet's own `examples/AzureFunctionsMesh` documents in detail: subscribing via
  `azure_function_endpoint` validates through an ARM control-plane lookup that's unreliable for a
  Consumption-plan custom handler until it's been published and invoked at least once. This
  stack's `deploy/main.tf` uses the same proven fix — subscribe via the Functions Event Grid
  extension's own webhook (validated against the *live* running function) — and the deploy
  workflow warms each consumer app immediately before the second `terraform apply`. If a
  subscription still fails validation, the consumer app was likely cold; warm it (hit any route)
  and re-run that apply.
- **The second apply must not strip the deployed package.** Linux Consumption's zip-deploy sets
  `WEBSITE_RUN_FROM_PACKAGE`, which isn't declared in `app_settings` — without
  `lifecycle { ignore_changes = [...] }` (already in `deploy/main.tf`, matching .NET's own fix),
  the Event-Grid-wiring apply would see it as drift and un-deploy every Function in the same pass.
- **Storage account name collisions.** `storage_account` must be globally unique across Azure, and
  the remote Terraform state uses `<storage_account>tfstate` — pick a name that doesn't collide
  with another example or another benzene port's own deployment.

## What is and isn't verified

`go build`/`go vet`/`gofmt` and full `go test ./... -race -cover` all pass repo-wide (see the
PR/commit description for the exact commands and counts), including
`azurefunctions.EventHubHandler`'s own new unit test suite and `meshapp`'s integration test
(`TestApp_AnnounceAndHeartbeat_ReportToARealCollector`) that drives `App.Announce`/`heartbeat`
against a **real** `meshd.Collector` behind an `httptest.Server` — the same proof shape
`examples/k8s-mesh-helloworld/cmd/service`'s and `examples/aws-lambda-mesh/meshapp`'s own chain
tests use. `deploy/build.sh` was run and its zips verified deployable-shaped (host.json + every
Function folder's `function.json` + one `handler` binary, matching the custom-handler package
layout). `terraform fmt -check -diff` passes on `deploy/`. `terraform init`/`validate` against the
real `hashicorp/azurerm` provider were **not** exercised — this sandbox's egress policy blocks
`registry.terraform.io`, which is expected and documented, not a defect in the configuration (the
same limitation every other Terraform-based mesh example in this repo notes for its own scope).
Actually deploying to Azure, and the first live run of the deploy/destroy GitHub Actions
workflows, are the genuine end-to-end proof; treat the first real run as that proof — in
particular the Event Grid webhook-subscription dance, which only a real Consumption-plan deploy
can prove out.
