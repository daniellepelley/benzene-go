# How Benzene compares — multi-cloud positioning

Where this project sits relative to the other ways teams build cloud-portable services in Go,
and when you'd pick each. This is a positioning document, not a feature race: every project
below is good at what it actually is, and several are complementary rather than competing.

The short version: Benzene is a **zero-dependency, library-embedded, cross-language
application framework** whose portability comes from a transport-neutral wire envelope
(`wire/`) plus thin per-platform adapters, rather than from a sidecar runtime (Dapr), a
resource-abstraction library (Go CDK), or a deployment platform (Encore).

## The comparison

| | Benzene | [Dapr](https://dapr.io/) | [Go CDK](https://gocloud.dev/) | [Watermill](https://watermill.io/) | [Encore.go](https://encore.dev/) |
|---|---|---|---|---|---|
| Model | Embedded library; portability via a wire envelope | Sidecar runtime + building-block HTTP/gRPC APIs | Portable resource APIs (blob, pubsub, SQL…) over per-provider drivers | Pub/sub message library with broker adapters | Infra-from-code framework + hosted platform |
| What it abstracts | The *application*: handlers, middleware, status vocabulary, discovery | Cloud building blocks behind runtime APIs | Individual cloud *resources* | Message transport | Infrastructure provisioning |
| Runs inside FaaS (Lambda / Functions) | Yes - first-class bindings | Awkward - the sidecar model assumes long-lived compute, usually Kubernetes | Yes (it's a library) | Yes (it's a library) | No - it provisions its own compute |
| Extra infrastructure to operate | None | Sidecar per app + control plane | None | None | Platform relationship (or self-managed via its CLI) |
| Cross-language wire contract | Yes - C# + Go, pinned by shared conformance fixtures | Yes - via the runtime's APIs, many SDKs | No | No | No |
| Service topology / observability built in | Yes - the mesh (`mesh`/`meshd`), no extra infra | Yes - needs the Dapr control plane | No | No | Yes - via the Encore platform |
| Dependency footprint | Zero in the core module | Heavy (runtime + control plane) | Moderate | Moderate | Framework + platform coupling |

## When to pick what

**Pick Dapr** when you're already on Kubernetes, want its very broad component catalog
(state stores, secrets, workflows, bindings for dozens of brokers), and are happy to operate
the runtime. Benzene's counter-pitch is Dapr's portability promise *without* the operational
tax - no sidecar, no control plane - and it works in exactly the environments the sidecar
model struggles with: serverless functions.

**Pick Go CDK** when the thing you need to make portable is a *resource* - a blob bucket, a
pub/sub topic, a SQL connection - inside an application whose architecture you're designing
yourself. Go CDK doesn't have (or want) an opinion about handlers, middleware, or
service-to-service contracts, which is precisely what Benzene provides. The two compose: a
Benzene handler can use a Go CDK `blob.Bucket` as one of its ports.

**Pick Watermill** when you need breadth of broker support today (Kafka, RabbitMQ, NATS,
Redis Streams, GCP Pub/Sub, SQS/SNS and more) for message *plumbing*. Watermill deliberately
stops at the transport layer: no request/response semantics, no cross-language contract, no
self-describing services. Benzene covers fewer brokers but carries a full application model
across the ones it covers.

**Pick Encore** when you want infrastructure provisioned from your code and are comfortable
with the framework and platform that implies (AWS and GCP only). Benzene has no provisioning
story at all - deployment is your build pipeline's job, documented per example - and in
exchange has no platform coupling and covers Azure.

**Pick Benzene** when you want the same handler code, wire contract, status vocabulary, and
observability to run on AWS Lambda, Azure Functions, Cloud Run, containers, or bare
`net/http` - including alongside services written in C# on the same wire contract - with
nothing added to your dependency graph and nothing new to operate.

## What makes the position defensible

- **The wire envelope, not the platform, is the abstraction.** Every binding
  (`awslambda`, `azurefunctions`, `awssqs`, `awssns`, `httpbinding`) is a thin adapter to and
  from `wire.Request`/`wire.Response`. No cloud SDK type appears outside the two isolated
  outbound-client modules. Swapping clouds changes the adapter, never the application.
- **Cross-language conformance is tested, not claimed.** The vendored fixtures in
  `conformance/` pin this port to the same language-neutral spec the C# implementation runs
  against, and the mesh has hosted live cross-language (C#↔Go) fleets.
- **Zero dependencies is a policy, not an accident** - see `CLAUDE.md` and `RELEASING.md`.
  The two packages that genuinely need an SDK live in their own modules so the exception
  can't spread.
- **Observability without an observability stack.** A Benzene fleet self-describes (topics,
  schemas, contract hashes) and self-reports (semantic traces with W3C `traceparent`), and
  the collector + Mesh View run as an ordinary Benzene service - deployable to any of the
  same targets.

## Where the honest gaps are

Kept current in `ROADMAP.md`; the headline ones for a multi-cloud evaluation:

- **Broker breadth.** SQS, SNS, Pub/Sub, and Kafka are covered (plus anything
  CloudEvents-shaped via the `cloudevents` package - Event Grid, Knative, EventBridge);
  gRPC still needs a dependency decision, and Watermill or Dapr cover more brokers
  (RabbitMQ, NATS, Redis Streams) today.
- **The deploy workflows are real but unexercised** - each cloud example documents exactly
  what was and wasn't verified without live credentials. Nothing here claims a deploy that
  didn't happen.
