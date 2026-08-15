# AWS Lambda mesh — six chained Cloud Service Lambdas + a push-based mesh

The AWS-deployable "mesh" estate for benzene-go: six domain Lambdas (orders, payments, shipping,
inventory, notifications, analytics) chained over SQS/SNS/EventBridge, plus a seventh **mesh**
Lambda that ingests their register/heartbeat/trace/issue reports and serves the fleet UI. It is
the Go counterpart of
[benzene-dotnet's `examples/AwsMesh`](https://github.com/daniellepelley/benzene-dotnet/tree/main/examples/AwsMesh)
and [benzene-typescript's `examples/aws-lambda-mesh`](https://github.com/daniellepelley/benzene-typescript/tree/main/examples/aws-lambda-mesh)
— same topology, same estate, **one deliberate divergence** (read the next section first). Where
[`examples/k8s-mesh-helloworld`](../k8s-mesh-helloworld) proves the mesh's push-based collector
pattern on Kubernetes, this example proves the same pattern on serverless Lambda.

## Divergence from .NET: push-only, no AWS Lambda discovery

benzene-dotnet's `examples/AwsMesh/Mesh` **discovers** the six service Lambdas by listing every
function in the account and reading their tags (`Benzene.Mesh.Discovery.Aws`'s
`AwsLambdaDiscoveryProvider`, `lambda:ListFunctions` + `lambda:ListTags`), then **interrogates**
each one over a synchronous Lambda Invoke on its `spec`/`healthcheck` topics, writing the
discovered registry plus the aggregated catalog to S3 (`Benzene.Mesh.Aws.S3`) — a *pull* model.
benzene-typescript's `examples/aws-lambda-mesh` follows the identical shape (see its own README
and `deploy/main.tf`): `AwsLambdaDiscoveryProvider`, `LambdaMeshServiceSource`, an
`S3MeshArtifactStore`, and a static catalog viewer served from an S3 website bucket.

**benzene-go has neither piece.** There is no `ListFunctions`/`ListTags` AWS Lambda discovery
provider anywhere in this port, and no S3-backed mesh artifact/catalog store — building either
from scratch would mean inventing a whole new discovery-source-plus-catalog-writer subsystem the
`mesh`/`meshd` packages don't have any abstraction for today, which is out of scope for one
example. What this port has instead, fully built and platform-agnostic, is
[`meshd`](../../meshd) — a **push**-based collector: services announce themselves and
heartbeat/trace/report-issues to it over the wire envelope
(`benzene:mesh:register`/`:heartbeat`/`:traces`/`:issues`), exactly the mechanism
[`examples/k8s-mesh-helloworld`](../k8s-mesh-helloworld) already proves end to end on Kubernetes.

So this example's mesh Lambda is **push-only**, and Lambda's serverless execution model shapes
three concrete implementation choices beyond what the Kubernetes example needed:

1. **Direct Lambda Invoke instead of HTTP.** Each of the six service Lambdas pushes its
   register/heartbeat/trace/issue reports **directly to the mesh Lambda via a synchronous Lambda
   Invoke**, using [`awslambdaclient.Client`](../../awslambdaclient) as the underlying sender —
   it already satisfies `mesh.Sender`'s `Send(ctx, topic, headers, message) Result[json.RawMessage]`
   signature with zero adapter code needed. [`meshapp.App`](meshapp/meshapp.go) wires
   `mesh.PushExporter`/`mesh.PushIssueExporter` around that client exactly the way
   `examples/k8s-mesh-helloworld/cmd/service` wires them around an `httpclient.Client` — same
   pattern, different transport. The mesh Lambda's own handler
   ([`cmd/mesh/main.go`](cmd/mesh/main.go)) is a thin wrapper around `meshd.Collector`, reused
   verbatim from the Kubernetes example (same `New`, same `Builder`, same `ViewHandler`) — **zero
   new framework code** in `mesh`/`meshd`/`awslambdaclient` was needed for this example.
2. **A fresh trace/issue exporter pair every invocation.** `mesh.PushExporter`/`PushIssueExporter`
   batch on a background goroutine and flush on a `BatchSize` trip or a `FlushInterval` tick —
   both timers that only advance while the process is scheduled. A Lambda execution environment is
   **frozen**, not merely idle, between invocations, so a long-lived exporter's ticks would often
   never fire before the process freezes, leaving an invocation's trace silently stuck in the
   queue (the feed is deliberately lossy by design — see `mesh.PushExporter`'s own doc comment).
   To keep the demo deterministic, `meshapp.App.Handler` builds a **fresh** exporter pair per
   invocation (`BatchSize: 1`, so the one event from this invocation flushes immediately) and
   `Close()`s them — a synchronous, blocking flush — before the handler returns. Only the
   `Pipeline` is rebuilt this way; the `Registry` and the derived `Descriptor` (schema derivation
   is startup-only, never on the dispatch path, matching every other package in this repo) are
   built once at cold start and reused for the life of the execution environment. See
   [`meshapp/meshapp.go`](meshapp/meshapp.go)'s package doc for the full reasoning.
3. **`reserved_concurrent_executions = 1` on the mesh Lambda.** `meshd.Collector`'s in-memory
   state does not survive a cold start and is **not shared** across concurrent warm instances of
   the mesh function — two concurrent invocations of an unpinned mesh Lambda would each hold their
   own, partial view of the fleet. Pinning concurrency to 1 in `deploy/main.tf`
   (`var.mesh_reserved_concurrency`, default `1`) makes AWS serialize every invocation of the mesh
   function through the same warm instance whenever possible, so the collector behaves as the one
   consistent store the demo needs. This is a genuine tradeoff, not a free lunch: it caps the mesh
   Lambda's own throughput to whatever one instance can serve (the six service Lambdas pushing to
   it stay independently, fully concurrent — only the mesh's own fan-in is serialized), and it is
   **not** a pattern to copy for a mesh handling production-scale fleet traffic. For a demo estate
   built to prove the mesh's live plane end to end, honestly and deterministically, that trade
   reads as a fair one — the same stance [`examples/k8s-mesh-helloworld`'s own "Divergence from
   .NET" section](../k8s-mesh-helloworld/README.md) takes for its own scope tradeoff, and see
   [`meshd`'s package doc](../../meshd/meshd.go) and [`CLAUDE.md`](../../CLAUDE.md)'s `meshd/`
   entry for where this divergence is also recorded at the repo level.

Net effect: you get a fully live mesh — real registered services, real health, real trace flows
from the orders → payments → shipping chain and every fan-out below — deployed as plain Lambda
functions with no discovery IAM (`lambda:ListFunctions`/`ListTags`) anywhere in the stack, and no
S3 catalog bucket. What you *don't* get is a catalog of services that exist but have never pushed
a report (a deployed-but-never-invoked Lambda is invisible here; in .NET/TypeScript it would still
show up, discovered-but-uninterrogated) — the same fair trade `examples/k8s-mesh-helloworld`
already makes for Kubernetes.

## The estate

```
  orders ──payments:capture (SQS)──▶ payments ──shipping:book (SQS)──▶ shipping
    │                                    │                                 │
    └─order:placed (SNS)─▶ inventory,    ├─payment:captured (EventBridge)─▶ analytics, notifications
                          notifications  │
                                         └─ shipment:dispatched (EventBridge) ─▶ inventory, notifications, analytics

  Every one of the six Lambdas above ALSO pushes register/heartbeat/traces/issues directly to:

  orders, payments, shipping, inventory, notifications, analytics
      │  (synchronous Lambda Invoke, awslambdaclient.Client)
      ▼
    mesh   ── meshd.Collector (in-memory, reserved_concurrent_executions=1) ──▶ GET /benzene/fleet-ui
```

Each service is **one Lambda** — a composite handler ([`meshapp.App.Handler`](meshapp/meshapp.go))
that classifies the raw event by its shape (API Gateway, SQS, SNS, or EventBridge) and dispatches
to the matching `awslambda`/`awssqs`/`awssns`/`awseventbridge` adapter, reusing this repo's
existing bindings unchanged:

| Service | Trigger(s) | Registers | Publishes |
|---|---|---|---|
| `orders` | API Gateway (`POST /orders`) | `order:create` | `payments:capture` (SQS), `order:placed` (SNS) |
| `payments` | SQS (`payments:capture`) | `payments:capture` | `shipping:book` (SQS), `payment:captured` (EventBridge) |
| `shipping` | SQS (`shipping:book`) | `shipping:book` | `shipment:dispatched` (EventBridge) — terminal, no further hop |
| `inventory` | SNS + EventBridge | `order:placed`, `shipment:dispatched` | — (pure consumer) |
| `notifications` | SNS + EventBridge | `order:placed`, `payment:captured`, `shipment:dispatched` | — (pure consumer) |
| `analytics` | EventBridge | `payment:captured`, `shipment:dispatched` | — (pure consumer) |
| `mesh` | API Gateway + direct Lambda Invoke | `benzene:mesh:*` (via `meshd.Collector`) | — |

Domain logic is deliberately trivial (see [`domain/domain.go`](domain/domain.go)) — the point of
this example is proving the mesh's transport wiring, not rich business behaviour, matching
benzene-typescript's `functions/*.ts` and `examples/k8s-mesh-helloworld/domain`'s own stance.

## Projects

| Path | What it is |
|---|---|
| `domain/` | the trivial handlers for all six services — the Go counterpart of benzene-typescript's `src/services.ts` |
| `meshapp/` | the shared composite-Lambda composition root every service Lambda is built from: event-shape classification, fleet-reporting wiring, the per-invocation trace/issue flush (see its package doc) |
| `cmd/orders/`, `cmd/payments/`, `cmd/shipping/`, `cmd/inventory/`, `cmd/notifications/`, `cmd/analytics/` | one binary per service Lambda — thin `main.go`s wiring real AWS SDK clients into `meshapp.App` |
| `cmd/mesh/` | the mesh Lambda: wraps `meshd.Collector`, answers both a direct Lambda Invoke (register/heartbeat/traces/issues) and an API-Gateway-fronted HTTP request (the Mesh View, its envelope-over-HTTP polling endpoint, and `/mesh/discovered`) |
| `deploy/` | Terraform for the whole estate (Lambdas, SQS, SNS, EventBridge, API Gateway, IAM) + `build.sh` to cross-compile and zip the seven binaries |
| `.github/workflows/mesh-example-aws-lambda-deploy.yml` | build zips → terraform apply → kick off the cascade → assert 6 discovered |
| `.github/workflows/mesh-example-aws-lambda-destroy.yml` | terraform destroy |

This is its own Go module (see `go.mod`) — like `examples/aws-sqs-helloworld`/`aws-sns-helloworld`,
it depends on the root module plus the `awssqs`, `awssns`, `awseventbridge`, and `awslambdaclient`
modules, so putting it inside any one of those would create a dependency cycle.

## Deploy to AWS

Requires Go, [Terraform](https://developer.hashicorp.com/terraform/install) ≥ 1.5, and AWS
credentials configured (`AWS_PROFILE` or `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`).

```bash
cd examples/aws-lambda-mesh
deploy/build.sh                 # cross-compiles + zips all 7 binaries into deploy/build/*.zip
cd deploy
terraform init                  # local state by default; add -backend-config=... for S3 (see the workflow)
terraform apply
```

### From GitHub Actions

**Actions → Mesh Example AWS Lambda Deploy → Run workflow.** Requires repo secrets
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` with permission to manage Lambda, IAM, SQS, SNS,
EventBridge, and API Gateway (this repo's existing convention — see
`deploy-aws-lambda-helloworld.yml`). It builds the seven zips, keeps Terraform state in a
per-account S3 bucket (`benzene-go-lambda-mesh-tfstate-<account>`, created on first run, key
`aws-lambda-mesh/`), applies the stack, `POST`s one order to kick off the cascade, and polls
`GET /mesh/discovered` until it reads `{"discovered":6}` — a real end-to-end proof of the
push-based fleet story. **Actions → Mesh Example AWS Lambda Destroy → Run workflow** tears it back
down.

## Try it

```bash
# 1. Kick off the cascade
curl -X POST "$(terraform output -raw orders_url)" \
  -H 'content-type: application/json' -d '{"customerId":"cust-1","sku":"espresso","quantity":2}'
# => {"orderId":"order-1","status":"created"}

# 2. Watch the estate register itself (populated within a couple of invocations)
curl "$(terraform output -raw discovered_url)"
# => {"discovered":6}

# 3. Open the Mesh View in a browser
terraform output -raw mesh_ui_url
# => https://<api-id>.execute-api.<region>.amazonaws.com  (append /benzene/fleet-ui)
```

The cascade (`orders → payments → shipping` over SQS, plus the SNS + EventBridge fan-outs) runs
asynchronously across the real Lambdas; each function's CloudWatch Logs shows it being reached,
and the Mesh View's flow explorer shows the trace once each hop has pushed its trace event.

## Known first-deploy iteration points

- **Cold-start announce.** `meshapp.App.Announce` retries with a short backoff (5 attempts, 500ms
  apart — a tighter budget than `examples/k8s-mesh-helloworld`'s 30×2s, since it runs once inline
  at Lambda cold start rather than in a background loop) before giving up; a service Lambda that
  has never been invoked has never had the chance to announce at all — invoke it once (its API
  Gateway route, or any of its triggering queues/topics/rules) to bring it into the fleet.
- **`reserved_concurrent_executions = 1` and account-level unreserved concurrency.** Every AWS
  account has a pool of unreserved concurrency shared across every Lambda function that doesn't
  reserve its own; pinning the mesh Lambda to exactly 1 reserved execution slot subtracts 1 from
  that shared pool. On a heavily-used account this is negligible; on a fresh account with a low
  account-wide concurrency limit, double-check the six service Lambdas (which are NOT
  concurrency-pinned) still have enough of the shared pool to run under load.
- **Go base image / architecture** — `deploy/build.sh` defaults to `GOARCH=arm64`, matching
  `variables.tf`'s `lambda_architecture` default; pass a different arch as its one argument
  (`deploy/build.sh amd64`) and set `-var lambda_architecture=x86_64` to match if your account's
  Lambda quota or tooling prefers x86_64.

## What is and isn't verified

`go build`/`go vet`/`gofmt` and `go test ./... -race -cover` all pass for every package here (see
the PR/commit description for the exact commands and counts), including an in-process integration
test (`meshapp/meshapp_test.go`'s `TestApp_AnnounceAndHandler_ReportToARealCollector`) that drives
`meshapp.App.Announce` and `Handler` against a **real** `meshd.Collector` through a fake Lambda
Invoke API — the same proof shape `examples/k8s-mesh-helloworld/cmd/service`'s chain test uses,
adapted for the Lambda-invoke transport instead of HTTP. `deploy/build.sh` was run and its zips
verified deployable-shaped (single `bootstrap` binary per zip, well under Lambda's size limits).
`terraform fmt -check -diff` passes on `deploy/`. `terraform init`/`validate` against the real
`hashicorp/aws` provider were **not** exercised — this sandbox's egress policy blocks
`registry.terraform.io`, which is expected and documented, not a defect in the configuration (the
same limitation `examples/k8s-mesh-helloworld` notes for its own Terraform). Actually deploying to
AWS, and the first live run of the deploy/destroy GitHub Actions workflows, are the genuine
end-to-end proof; treat the first real run as that proof.
