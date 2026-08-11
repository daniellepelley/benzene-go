# Getting Started: Benzene on Kubernetes

This guide takes you from an empty folder to **one handler running as three independent Kubernetes
Deployments** — an HTTP API, an SQS worker, and a Kafka worker — all dispatching into the exact same
function. That's deliberately more than "deploy a `net/http` server to a pod": see
[Why not just net/http?](getting-started.md#why-not-just-nethttp) for why a single-transport example
wouldn't actually show what Benzene is for here.

> **Runnable version:** this guide follows
> [`examples/k8s-helloworld`](../examples/k8s-helloworld) — Dockerfiles, Kubernetes manifests, and a
> `docker-compose.yml` that runs all three legs locally against LocalStack + a throwaway Kafka
> broker, no cloud account needed.

## What you'll build

```
                              ┌──────────────────────────────────────┐
        HTTP  ──────────────▶│  greet-api           (Deployment)     │──┐
                              └──────────────────────────────────────┘  │
                              ┌──────────────────────────────────────┐  │   all three dispatch
        SQS queue  ─────────▶│  greet-sqsworker     (Deployment)     │──┼──▶ greeting.Handler
                              └──────────────────────────────────────┘  │
                              ┌──────────────────────────────────────┐  │
        Kafka topic  ───────▶│  greet-kafkaworker   (Deployment)     │──┘
                              └──────────────────────────────────────┘
```

One handler package, imported by three separate `main` packages, each its own container image, each
its own Kubernetes Deployment, each independently replicated and scaled.

## Prerequisites

- Go 1.24+ and Docker.
- A cluster and `kubectl` — [kind](https://kind.sigs.k8s.io/) is the quickest for local work
  (`kind create cluster`).
- To follow along with real messages rather than just reading: an SQS queue and a Kafka topic
  somewhere reachable (LocalStack and a throwaway broker via `docker compose` cover both with no
  account at all — see the [runnable example](../examples/k8s-helloworld)).

## 1. The shared handler

Everything downstream imports this one package. This port has no reflection-based handler
discovery (see [Getting started](getting-started.md#2-write-a-handler)), so "shared" means a plain
function each host registers explicitly, not something auto-discovered:

```go
// greeting/greeting.go
package greeting

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

type GreetRequest struct {
	Name string `json:"name"`
}

type GreetResponse struct {
	Greeting string `json:"greeting"`
}

func Handler(_ context.Context, req GreetRequest) benzene.Result[GreetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[GreetResponse]("name is required")
	}
	return benzene.Ok(GreetResponse{Greeting: "Hello, " + req.Name + "!"})
}
```

Nothing here mentions Kubernetes, SQS, Kafka, or HTTP status codes — that's the point of a message
handler in Benzene's hexagonal architecture: the domain logic sits behind a port, and a transport is
just an adapter in front of it.

## 2. Host it over HTTP

```go
// api/main.go
package main

import (
	"log"
	"net/http"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"

	"github.com/.../greeting" // your module path
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func main() {
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	handler := httpbinding.Handler(newApp(), routes)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
```

This is exactly [Getting started](getting-started.md) — nothing here is Kubernetes-specific yet.

## 3. Host it on SQS

A second, completely independent `main` package, sharing nothing with `api` except the import of
`greeting`:

```go
// sqsworker/main.go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awssqs"

	"github.com/.../greeting"
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	consumer := &awssqs.Consumer{
		API:      sqs.NewFromConfig(cfg), // default credential chain - an IRSA role on EKS
		QueueURL: os.Getenv("QUEUE_URL"),
		Builder:  newApp(),
	}
	if err := consumer.Validate(); err != nil {
		log.Fatalf("consumer: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
```

`awssqs.Consumer` is a long-running poller — the self-hosted counterpart of the Lambda-trigger
`awssqs.Handler`, and the right shape for a pod that stays up. It long-polls the queue, runs each
message through the same pipeline `api` uses, and deletes only the messages that succeeded — a
failed message is left on the queue individually for redelivery/DLQ redrive rather than lost with
the rest of the batch.

## 4. Host it on Kafka

A third `main` package, independent of the other two:

```go
// kafkaworker/main.go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/kafka"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/.../greeting"
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func main() {
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: []string{os.Getenv("KAFKA_BROKERS")},
		GroupID: "k8s-helloworld",
		Topic:   "greet",
	})
	defer reader.Close()

	consumer := &kafka.Consumer{Source: reader, Builder: newApp()}
	if err := consumer.Validate(); err != nil {
		log.Fatalf("consumer: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
```

See [Kafka Setup](getting-started-kafka.md) for why one Kafka topic is one Benzene topic here (no
attribute/header indirection the way SQS/HTTP have one) — that's why `"greet"` is both the registered
topic and the literal Kafka topic name.

## 5. Containerise all three

Each binary gets its own `Dockerfile`, built with the module's `go.work`-resolved dependencies:

```dockerfile
# Dockerfile.api
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./api

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/api /api
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/api"]
```

`Dockerfile.sqsworker` and `Dockerfile.kafkaworker` follow the same shape, swapping the build path —
a worker has no inbound listener, so there's no `PORT`/`EXPOSE` to set.

```bash
docker build -f Dockerfile.api         -t greet-api:local         .
docker build -f Dockerfile.sqsworker   -t greet-sqsworker:local   .
docker build -f Dockerfile.kafkaworker -t greet-kafkaworker:local .
kind load docker-image greet-api:local greet-sqsworker:local greet-kafkaworker:local
```

## 6. Deploy all three

`greet-api` gets a `Deployment` + `Service`, same as any HTTP workload. The two workers get a
`Deployment` each and **no** `Service` — nothing calls a worker pod, it calls out:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: greet-api
spec:
  replicas: 2
  selector: { matchLabels: { app: greet-api } }
  template:
    metadata: { labels: { app: greet-api } }
    spec:
      containers:
        - name: greet-api
          image: greet-api:local
          ports: [{ containerPort: 8080 }]
          env: [{ name: PORT, value: "8080" }]
          readinessProbe: { tcpSocket: { port: 8080 }, initialDelaySeconds: 3 }
---
apiVersion: v1
kind: Service
metadata:
  name: greet-api
spec:
  selector: { app: greet-api }
  ports: [{ port: 80, targetPort: 8080 }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: greet-sqsworker
spec:
  replicas: 1
  selector: { matchLabels: { app: greet-sqsworker } }
  template:
    metadata: { labels: { app: greet-sqsworker } }
    spec:
      containers:
        - name: greet-sqsworker
          image: greet-sqsworker:local
          env: [{ name: QUEUE_URL, value: "https://sqs.eu-west-1.amazonaws.com/<account-id>/greet" }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: greet-kafkaworker
spec:
  replicas: 1
  selector: { matchLabels: { app: greet-kafkaworker } }
  template:
    metadata: { labels: { app: greet-kafkaworker } }
    spec:
      containers:
        - name: greet-kafkaworker
          image: greet-kafkaworker:local
          env: [{ name: KAFKA_BROKERS, value: "kafka-bootstrap.kafka.svc.cluster.local:9092" }]
```

```bash
kubectl apply -f k8s.yaml
kubectl get pods   # 4 pods: 2x greet-api, 1x greet-sqsworker, 1x greet-kafkaworker
```

## 7. Watch the same handler run three ways

```bash
kubectl port-forward service/greet-api 8080:80 &
curl -XPOST localhost:8080/greet -H 'content-type: application/json' -d '{"name":"world"}'
```

Send a message to the SQS queue or the Kafka topic directly (see [the runnable
example](../examples/k8s-helloworld) for exact commands against a local LocalStack/Kafka pair) and
the **same handler** runs, for a request that never touched HTTP — `kubectl logs
deploy/greet-sqsworker` shows it. That's the proof: one handler, three independently deployed,
independently scaled entry points.

```bash
kubectl scale deploy/greet-kafkaworker --replicas=3   # only the Kafka leg scales
```

## Next steps

- **Why this shape at all** — [Why not just net/http?](getting-started.md#why-not-just-nethttp).
- **More self-hosted workers** — [Kafka Setup](getting-started-kafka.md) covers `kafka.Consumer` in
  depth; `awssqs.Consumer` is documented in its own package doc comment. Both are worth reaching for
  even as a service's *only* transport, since neither's raw client gives you routing or a middleware
  pipeline the way `net/http` gives HTTP.
- **The cloud hosts** — [AWS Lambda](getting-started-aws.md) and
  [Azure Functions](getting-started-azure.md) run the same handler behind a managed event source
  instead of a self-hosted poller.
