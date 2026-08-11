# One handler, three Kubernetes Deployments

The runnable version of [Getting Started: Benzene on Kubernetes](../../docs/getting-started-kubernetes.md).

The same `greeting.Handler` — the shared package every binary below imports — reached three
independent ways, each its own pod:

```
                              ┌──────────────────────────────────────┐
        HTTP  ──────────────▶│  greet-api           (Deployment)     │──┐
                              └──────────────────────────────────────┘  │
                              ┌──────────────────────────────────────┐  │   all three dispatch
        SQS queue  ─────────▶│  greet-sqsworker     (Deployment)     │──┼──▶ greeting.Handler
                              └──────────────────────────────────────┘  │   (greeting/)
                              ┌──────────────────────────────────────┐  │
        Kafka topic  ───────▶│  greet-kafkaworker   (Deployment)     │──┘
                              └──────────────────────────────────────┘
```

Nothing in the handler knows which pod called it. That's the point: the same business logic scales,
deploys, and rolls back independently behind whichever transport actually reaches it — a bare
`net/http` handler alone gives you the first Deployment; Benzene gives you all three from one
function.

## Binaries

This is one Go module with three `main` packages, mirroring `examples/aws-sqs-helloworld`'s
consumer/publisher split:

| Path | What it is |
|---|---|
| `greeting/` | the shared handler - `Handler(ctx, GreetRequest) benzene.Result[GreetResponse]`, imported by all three binaries below |
| `api/` | a plain `net/http` server (`httpbinding.Handler`) - `POST /greet` |
| `sqsworker/` | `awssqs.Consumer` - the self-hosted SQS poller, not the Lambda-trigger `awssqs.Handler` |
| `kafkaworker/` | `kafka.Consumer` - the self-hosted Kafka consumer |
| `k8s/` | three Deployments (`api.yaml` also a Service), pointed at a real SQS queue and Kafka cluster via env vars - no bundled infra |
| `compose/` | `docker-compose.yml` - LocalStack (SQS) + a throwaway Kafka broker + all three binaries, for a credential-free local run |

Every binary registers the handler itself (`benzene.Register(registry, benzene.NewTopic("greet"),
...)`) — this port has no reflection-based handler discovery, so "shared" means "the same function
imported three times and wired explicitly," not "auto-discovered." See each `main.go`'s `newApp()`.

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
# Kafka topic name here, unlike SQS/HTTP's attribute/route - see kafkaworker/main.go's comment).
docker exec -i $(docker compose -f examples/k8s-helloworld/compose/docker-compose.yml ps -q kafka) \
  kafka-console-producer --bootstrap-server localhost:29092 --topic greet <<< '{"name":"kafka"}'
```

`docker compose logs -f greet-api greet-sqsworker greet-kafkaworker` to watch all three at once — a
greeting placed through any of the three reaches the exact same handler.

## Deploy to Kubernetes

Build and load the three images (against a [kind](https://kind.sigs.k8s.io) cluster — swap for your
registry's push/pull on a real cluster):

```bash
docker build -f examples/k8s-helloworld/Dockerfile.api         -t k8s-helloworld-api:local         .
docker build -f examples/k8s-helloworld/Dockerfile.sqsworker   -t k8s-helloworld-sqsworker:local   .
docker build -f examples/k8s-helloworld/Dockerfile.kafkaworker -t k8s-helloworld-kafkaworker:local .
kind load docker-image k8s-helloworld-api:local k8s-helloworld-sqsworker:local k8s-helloworld-kafkaworker:local
```

Edit the placeholder env values in `k8s/sqsworker.yaml` and `k8s/kafkaworker.yaml` to point at a
real queue and cluster (there is deliberately no bundled SQS/Kafka in these manifests — see each
file's own comment for why, and for the IRSA note on the SQS side), then:

```bash
kubectl apply -k examples/k8s-helloworld/k8s/
kubectl -n k8s-helloworld get pods   # 4 pods: 2x greet-api, 1x greet-sqsworker, 1x greet-kafkaworker
kubectl -n k8s-helloworld logs -f deploy/greet-sqsworker
```

Scale the transports independently, because they're independent Deployments:

```bash
kubectl -n k8s-helloworld scale deploy/greet-kafkaworker --replicas=3
```

## Why this, and not just net/http

See [Why not just net/http?](../../docs/getting-started.md#why-not-just-nethttp) for the reasoning
this example exists to prove.
