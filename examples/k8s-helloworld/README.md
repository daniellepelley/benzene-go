# One handler, one binary, three transports

The runnable version of [Getting Started: Benzene on Kubernetes](../../docs/getting-started-kubernetes.md).

The same `greeting.Handler` reached three independent ways, from **one** running process:

```
        HTTP        ─────────┐
        SQS queue   ─────────┼──▶  k8s-helloworld (Deployment)  ──▶  greeting.Handler
        Kafka topic ─────────┘
```

Nothing in the handler knows which transport called it. That's the point: `main.go` runs a
`net/http` server, an SQS poller, and a Kafka consumer as three goroutines in the same process, all
dispatching into the exact same function — a bare `net/http` handler alone gives you the HTTP leg;
Benzene gives you all three from one binary, one image, one Deployment.

## Files

This is one Go module with a single `main` package - `greeting/` (the shared handler) plus `main.go`
(all three legs):

| Path | What it is |
|---|---|
| `greeting/` | the shared handler - `Handler(ctx, GreetRequest) benzene.Result[GreetResponse]` |
| `main.go` | one binary: a `net/http` server (`httpbinding.Handler`), `awssqs.Consumer` (the self-hosted SQS poller, not the Lambda-trigger `awssqs.Handler`), and `kafka.Consumer` (the self-hosted Kafka consumer) - each its own goroutine against a shared, SIGINT/SIGTERM-cancelled `context.Context` |
| `k8s/` | one Deployment + Service, pointed at a real SQS queue and Kafka cluster via env vars - no bundled infra |
| `compose/` | `docker-compose.yml` - LocalStack (SQS) + a throwaway Kafka broker + the one binary's image, for a credential-free local run |

`main.go` registers the handler three times (`benzene.Register(registry, benzene.NewTopic("greet"),
...)`, once per leg's own `newApp()`) — this port has no reflection-based handler discovery, so
"shared" means "the same function imported once, registered three times, wired explicitly," not
"auto-discovered."

Go has no equivalent of a "generic host" starting things sequentially the way .NET's does (see the
[Kubernetes guide](../../docs/getting-started-kubernetes.md) for that port's story) — three
goroutines, each running its own blocking `ListenAndServe`/`Consumer.Run` loop against a shared
`context.Context`, just works: the runtime schedules them independently, so a slow or stuck SQS poll
never delays the HTTP server (or vice versa). `main.go`'s comment block spells this out.

## Run it locally (no Kubernetes, no cloud account)

```bash
docker compose -f examples/k8s-helloworld/compose/docker-compose.yml up --build
```

Then, in three more terminals:

```bash
# 1. HTTP
curl -XPOST localhost:8080/greet -H 'content-type: application/json' -d '{"name":"world"}'
# {"greeting":"Hello, world!"}

# 2. SQS - send straight to the queue LocalStack created, no HTTP involved. `run --rm --entrypoint aws`
# starts a fresh throwaway container on the sqs-init service's image/network/credentials (that
# service's own container already exited once it finished creating the queue).
docker compose -f examples/k8s-helloworld/compose/docker-compose.yml run --rm --entrypoint aws sqs-init \
  --endpoint-url=http://localstack:4566 sqs send-message \
    --queue-url http://localstack:4566/000000000000/greet \
    --message-body '{"name":"sqs"}' \
    --message-attributes 'topic={StringValue=greet,DataType=String}'

# 3. Kafka - produce straight to the topic, no HTTP involved (the Benzene topic IS the literal
# Kafka topic name here, unlike SQS/HTTP's attribute/route - see main.go's comment).
docker exec -i $(docker compose -f examples/k8s-helloworld/compose/docker-compose.yml ps -q kafka) \
  kafka-console-producer --bootstrap-server localhost:29092 --topic greet <<< '{"name":"kafka"}'
```

Three different entry points, one container's logs - `docker compose logs -f k8s-helloworld` - proving
all three ran through the exact same handler function.

## Deploy to Kubernetes

Build and load the one image (against a [kind](https://kind.sigs.k8s.io) cluster — swap for your
registry's push/pull on a real cluster):

```bash
docker build -f examples/k8s-helloworld/Dockerfile -t k8s-helloworld:local .
kind load docker-image k8s-helloworld:local
```

Edit the placeholder `QUEUE_URL`/`KAFKA_BROKERS` values in `k8s/app.yaml` to point at a real queue and
cluster (there is deliberately no bundled SQS/Kafka in this manifest — see the file's own comment for
why, and for the IRSA note on the SQS side), then:

```bash
kubectl apply -k examples/k8s-helloworld/k8s/
kubectl -n k8s-helloworld get pods   # 2 pods: 2x k8s-helloworld
kubectl -n k8s-helloworld logs -f deploy/k8s-helloworld
```

There's only one Deployment to scale - scaling it scales all three transports' consuming capacity
together:

```bash
kubectl -n k8s-helloworld scale deploy/k8s-helloworld --replicas=4
```

## Why this, and not just net/http

See [Why not just net/http?](../../docs/getting-started.md#why-not-just-nethttp) for the reasoning
this example exists to prove.

## The alternative: one Deployment per transport

Combining all three transports into one binary is not the only valid shape - splitting them into
**separate** `main` packages/Deployments (one for HTTP, one for the SQS poller, one for the Kafka
consumer, each its own image) is a legitimate pattern too, and sometimes the better one: each
transport then scales, rolls back, and fails independently of the others. The tradeoff is real: more
images to build, more Deployments to manage, and (per the goroutine-per-leg structure above) a little
duplicated `newApp()`/wiring boilerplate per transport if they're split into separate binaries.
Reach for that shape instead when the transports' traffic, failure modes, or scaling needs genuinely
diverge - `greeting/greeting.go` doesn't change either way, only how many binaries and Dockerfiles
wrap it.
