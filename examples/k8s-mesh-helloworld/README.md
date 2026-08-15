# Kubernetes mesh — orders, payments, shipping, and a push-based mesh collector

The Kubernetes mesh estate for benzene-go: three domain services running as pods, chaining to
each other over HTTP, plus a **mesh service** that watches them live. It is the Go counterpart of
[benzene-dotnet's `examples/K8sMesh`](https://github.com/daniellepelley/benzene-dotnet/tree/main/examples/K8sMesh)
— same shape, same manifests structure, **one deliberate divergence** (read the next section
first). It runs two ways from the same manifests: credential-free on a throwaway
[`kind`](https://kind.sigs.k8s.io) cluster in CI, or on a real **AWS EKS** cluster with the Mesh
View on the public internet (see "Deploy to AWS (EKS)" below).

## Divergence from .NET: push-only, no Kubernetes API discovery

benzene-dotnet's `examples/K8sMesh/Mesh` **discovers** the three domain services by listing
`benzene`-labelled Kubernetes Services through the cluster API
(`Benzene.Mesh.Discovery.Kubernetes`), then **interrogates** each one over HTTP
(`GET /benzene/spec`, `GET /benzene/health`) — a *pull* model, needing a `ServiceAccount` +
`Role` + `RoleBinding` scoped to `list`/`get` on Services.

**benzene-go has no Kubernetes API service-discovery library** — no
`Benzene.Mesh.Discovery.Kubernetes` equivalent exists in this port, and building one from scratch
would be new framework capability, not an example backfill. What this port has instead, fully
built and platform-agnostic, is [`meshd`](../../meshd) — a **push**-based collector: services
announce themselves and heartbeat/trace/report-issues to it over the wire envelope
(`benzene:mesh:register`/`:heartbeat`/`:traces`/`:issues`), exactly the mechanism
[`examples/mesh-helloworld`](../mesh-helloworld) already proves end to end.

So this example's mesh service is **push-only**:

- The three domain services push register/heartbeat/trace/issue reports to the mesh's `meshd`
  collector over `MESH_COLLECTOR_ENVELOPE_URL` — the same env var and the same mechanism
  benzene-dotnet's services use for their **live Fleet plane** (the mesh pod's
  `Benzene.Mesh.Collector`). That half of the .NET example carries over unchanged.
- What does **not** carry over is the **pulled catalog** half: there is no Kubernetes
  `ServiceAccount`/`Role`/`RoleBinding` anywhere in `k8s/`, the mesh Deployment has no RBAC and
  never talks to the Kubernetes API, and nothing reads the `benzene: "true"` Service label (kept
  on the manifests only for documentation/future-proofing parity with the .NET shape).
- The mesh exposes `meshd`'s `benzene:mesh:query:fleet` topic (over its
  `POST /benzene/invoke` wire endpoint) and a convenience `GET /mesh/discovered` wrapper
  (`{"discovered":N}`) so a human or CI can assert a service count with one curl — the read side
  of the same push feed, not a triggered discovery pass (there's no pull side to trigger).
- It serves `meshd`'s existing Mesh View at `GET /benzene/fleet-ui`.

Net effect: you get a fully live mesh — real registered services, real health, real trace flows
from the orders → payments → shipping chain below — with no RBAC, no Kubernetes API client, and
about a third of the Go code the pull-based `Mesh/` project needed in .NET. What you *don't* get
is a catalog of services that exist but have never reported in (a service that's up but never
wired to `MESH_COLLECTOR_ENVELOPE_URL` is invisible here; in .NET it would still show up,
discovered-but-uninterrogated). For a demo estate built to prove the mesh's live plane, that
trade reads as a fair one — see [`meshd`'s own doc comment](../../meshd/meshd.go) and
[`CLAUDE.md`](../../CLAUDE.md)'s `meshd/` entry for where this divergence is also recorded at the
repo level.

## Architecture

```
        Kubernetes namespace: benzene-mesh
  ┌──────────┐   ┌───────────┐   ┌────────────┐
  │ orders   │──▶│ payments  │──▶│ shipping   │   3 Deployments (one image, MESH_SERVICE selects domain)
  │ Service  │   │ Service   │   │ Service    │   ──▶ POST /benzene/invoke  (a { topic, headers, body }
  └────┬─────┘   └─────┬─────┘   └─────┬──────┘        envelope, addressed by in-cluster DNS — the chain)
       │               │                │
       └───────────────┴────────────────┴──▶  each service PUSHES register + heartbeat + traces +
                                                issues to the mesh's collector (http://mesh/benzene/invoke)
                                                                     │
                                                                     ▼
                                                              ┌────────┐
                                                              │  mesh  │   meshd collector (in-memory) +
                                                              └────────┘   Mesh View at /benzene/fleet-ui
                                                                           — NodePort 30080, no RBAC
```

## Service-to-service calls — lightweight Benzene messages over HTTP

Beyond fleet reporting, each service **chains to the next** over its neighbour's wire-envelope
endpoint:

- **Ingress** — every service mounts `httpbinding.EnvelopeHandler` at `httpbinding.EnvelopePath`
  (`POST /benzene/invoke`, the .NET example's `/benzene-message` under a different, this port's
  standard name). A `{ topic, headers, body }` envelope POSTed there is routed to the service's
  handler by the envelope's topic, exactly as a queue or a Lambda invoke would — one endpoint
  serves every topic, no per-route REST contract. Plus `GET /benzene/health` (Cloud Service
  Profile) and a small native route per service for convenience (`POST /orders`,
  `POST /payments`, `POST /shipments`).
- **Egress** — `orders`' `order:create` handler asks `payments` to `payment:take`, and
  `payments`' `payment:take` handler asks `shipping` to `shipment:book`. Each caller declares
  that outbound call via `mesh.RegisterOutbound` (`registerDomain` in `cmd/service/main.go`,
  mesh.md §2.3) — that declared `consumes` entry is what makes the caller show up as the
  downstream topic's consumer on the mesh's topic catalog (mesh.md §4), with no traffic required.
  The `httpclient.Client` making the call is separately wrapped in `mesh.WithTraceContext`
  (propagates the invocation's span as a `traceparent` header, the same decorator
  `examples/mesh-helloworld`'s frontdoor → greeter hop uses) — that only lets the collector show
  the already-declared edge as *observed* (mesh.md §4.2), on top of, never instead of, the
  declared one. The target is the neighbour's in-cluster DNS name, injected as
  `DOWNSTREAM_MSG_URL` (e.g. `http://payments/benzene/invoke`); the terminal `shipping` service
  has none, and registers no outbound record.

Send an order into the front of the chain and watch it propagate (from a
`kubectl -n benzene-mesh port-forward svc/orders 8081:80`, or directly against a service ELB on
EKS):

```bash
curl -XPOST localhost:8081/orders -H 'content-type: application/json' \
     -d '{"customerId":"cust-1","sku":"espresso","quantity":2}'
# => {"orderId":"order-1","status":"created"}

# Or hit any service's envelope endpoint directly, addressing a topic it owns:
curl -XPOST localhost:8081/benzene/invoke -H 'content-type: application/json' \
     -d '{"topic":"payment:take","headers":{},"body":"{\"orderId\":\"o-9\",\"amount\":30}"}'
```

## Projects

| Path | What it is |
|---|---|
| `domain/` | the three tiny handlers (order:create, payment:take, shipment:book) — the Go counterpart of benzene-dotnet's `Service/Domain.cs` |
| `cmd/service/` | one binary/image; `MESH_SERVICE` picks the domain (orders/payments/shipping) at startup |
| `cmd/mesh/` | wraps `meshd.New(...)`: the envelope endpoint, the Mesh View, and the `/mesh/discovered` convenience wrapper — see "Divergence from .NET" above |
| `k8s/` | manifests: namespace, the 3 Deployments+Services, the mesh Deployment+Service (NodePort, no ServiceAccount/RBAC), a kustomize base |
| `deploy/` | Terraform for the AWS leg: EKS cluster + node group + the two ECR repositories |
| `deploy/eks/` | kustomize overlay over `k8s/`: ECR images (set by the workflow) + LoadBalancer Services |
| `.github/workflows/deploy-k8s-mesh-helloworld.yml` | build images → kind → deploy → assert 3 registered |
| `.github/workflows/deploy-eks-mesh-helloworld.yml` | terraform apply → push images to ECR → deploy → assert 3 registered → print the public URLs |

## Run it in CI (no credentials)

**Actions → Deploy K8s Mesh helloworld → Run workflow.** It builds both images, creates a `kind`
cluster, loads the images, applies the manifests, waits for rollout, then polls
`GET /mesh/discovered` until it reads `{"discovered":3}` — a real end-to-end proof of the
push-based fleet story — and exercises the orders → payments → shipping chain.

## Run it locally

You need Docker and [`kind`](https://kind.sigs.k8s.io) for this (not available in every
environment — see the note at the bottom of this section):

```bash
kind create cluster --name benzene
docker build -f examples/k8s-mesh-helloworld/Dockerfile.service -t benzene-k8smesh-service:local .
docker build -f examples/k8s-mesh-helloworld/Dockerfile.mesh    -t benzene-k8smesh-mesh:local .
kind load docker-image benzene-k8smesh-service:local --name benzene
kind load docker-image benzene-k8smesh-mesh:local     --name benzene
kubectl apply -k examples/k8s-mesh-helloworld/k8s/   # -k: the directory is a kustomize base (deploy/eks overlays it)

kubectl -n benzene-mesh port-forward svc/mesh 8080:80
# then, in another shell:
curl localhost:8080/mesh/discovered        # {"discovered":3} once all three have announced
open http://localhost:8080/benzene/fleet-ui  # the Mesh View — live services, health, flows
```

Each service's own derived spec is reachable the same way
(`kubectl -n benzene-mesh port-forward svc/orders 8081:80` → `curl localhost:8081/benzene/spec`).

## Deploy to AWS (EKS)

**Actions → Deploy EKS Mesh helloworld → Run workflow.** Requires repo secrets
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` with EKS, EC2, and ECR permissions (this repo's
existing convention — see `deploy-aws-lambda-helloworld.yml` — rather than benzene-dotnet's
GitHub-Environment setup), and a per-account S3 state bucket (key `k8s-mesh/`). The workflow:

1. `terraform apply` on `deploy/` — an EKS cluster (`benzene-go-k8smesh`) with one small managed
   node group on the account's default VPC, plus two ECR repositories. First-time cluster
   creation takes ~10–15 minutes.
2. builds the two images and pushes them to ECR, tagged with the commit SHA.
3. applies the **unchanged** `k8s/` manifests through the `deploy/eks` kustomize overlay, which
   swaps in the ECR images and turns the mesh's NodePort Service into an internet-facing
   **LoadBalancer** — and does the same for each `benzene`-labelled Service, so
   orders/payments/shipping are directly callable from the internet as well.
4. waits for the ELBs, polls `GET /mesh/discovered` until `{"discovered":3}`, and prints
   `http://<elb-hostname>/benzene/fleet-ui` (the Mesh View) plus each service's
   `http://<elb-hostname>/benzene/spec` URL (all in the run summary).

**Costs & teardown:** an EKS control plane bills ~$0.10/hour plus two `t3.small` nodes and four
classic ELBs (mesh + the three services, one per LoadBalancer Service). Re-run the workflow with
**destroy = true** to tear it all down (it deletes the namespace first so Kubernetes releases the
ELBs, then `terraform destroy`). Note the services are exposed **unauthenticated** — fine for
this throwaway demo, not a pattern to copy for real workloads.

To deploy from a laptop instead of CI, run the same four steps by hand: `terraform apply` in
`deploy/`, push the images to the ECR repositories it outputs, `aws eks update-kubeconfig`, then
`kustomize edit set image` + `kubectl apply -k` in `deploy/eks` (the workflow is the reference
script for the exact commands).

## What is and isn't verified

`go build`/`go vet`/`gofmt` and `kustomize build` + `kubectl apply --dry-run=client` (against a
minimal local discovery stub, since this environment has no live cluster) all pass for every
manifest and package here — see the PR/commit description for the exact commands. Actually
running the container images, deploying to `kind`, and deploying to EKS were **not** exercised by
whoever wrote this example (no Docker/kind/live cluster in that environment) — the first real run
of this example, in `kind` or on EKS, is the genuine end-to-end proof; treat the first CI run as
that proof, and watch its "Dump diagnostics on failure" step if anything doesn't come up clean.

## Known first-deploy iteration points

- **Go base image / distroless** — the Dockerfiles use `golang:1.24` to build and
  `gcr.io/distroless/static-debian12` to run; adjust the build-stage tag if your registry
  mirrors a different Go version.
- **Announce timing** — a service retries registration every 2s for up to 60s before giving up
  (still serving traffic throughout); if `/mesh/discovered` reads fewer than 3 shortly after
  rollout, give it a few more seconds before treating it as a real failure — both workflows
  already poll rather than checking once.
