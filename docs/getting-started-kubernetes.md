# Getting Started: Benzene on Kubernetes

This guide takes you from an empty folder to **one handler, reached over HTTP, SQS, and Kafka, hosted
in a single binary** — one `main.go`, one Docker image, one Kubernetes Deployment, dispatching every
message from all three transports into the exact same function. That's deliberately more than "deploy
a `net/http` server to a pod": see [Why not just net/http?](getting-started.md#why-not-just-nethttp)
for why a single-transport example wouldn't actually show what Benzene is for here.

> **Runnable version:** this guide follows [`examples/k8s-helloworld`](../examples/k8s-helloworld) —
> a Dockerfile, a Kubernetes manifest, and a `docker-compose.yml` that runs all three legs locally
> against LocalStack + a throwaway Kafka broker, no cloud account needed.

## What you'll build

```
        HTTP        ─────────┐
        SQS queue   ─────────┼──▶  k8s-helloworld (Deployment)  ──▶  greeting.Handler
        Kafka topic ─────────┘
```

One handler package, imported once by one `main` package that runs a `net/http` server, an SQS
poller, and a Kafka consumer as three goroutines — one container image, one Kubernetes Deployment.

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
function each leg registers explicitly, not something auto-discovered:

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

## 2. Host all three in one binary

Go has no "generic host" that starts things sequentially the way .NET's does — a Go process is just
`main()` and whatever goroutines it launches, scheduled independently by the runtime. So there's no
equivalent of the gotcha the .NET port's version of this guide has to work around (a self-hosted
worker's `StartAsync` starving Kestrel's own startup): a blocking `awssqs.Consumer.Run` alongside a
blocking `http.ListenAndServe`, each in its own goroutine, just works.

```go
// main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/kafka"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/.../greeting" // your module path
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	benzene.MustRegister(registry, benzene.NewTopic("greet"), greeting.Handler)
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- SQS leg ---
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}
	sqsConsumer := &awssqs.Consumer{
		API:      sqs.NewFromConfig(cfg), // default credential chain - an IRSA role on EKS
		QueueURL: os.Getenv("QUEUE_URL"),
		Builder:  newApp(),
	}
	if err := sqsConsumer.Validate(); err != nil {
		log.Fatalf("sqs consumer: %v", err)
	}

	// --- Kafka leg ---
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: []string{os.Getenv("KAFKA_BROKERS")},
		GroupID: "k8s-helloworld",
		Topic:   "greet",
	})
	defer reader.Close()
	kafkaConsumer := &kafka.Consumer{Source: reader, Builder: newApp()}
	if err := kafkaConsumer.Validate(); err != nil {
		log.Fatalf("kafka consumer: %v", err)
	}

	// --- HTTP leg ---
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	server := &http.Server{
		Addr:              ":" + os.Getenv("PORT"),
		Handler:           httpbinding.Handler(newApp(), routes),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Each leg runs in its own goroutine against the shared ctx, cancelled together on
	// SIGINT/SIGTERM. errCh carries the first fatal error from any leg.
	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := sqsConsumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- fmt.Errorf("sqs: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := kafkaConsumer.Run(ctx); err != nil {
			errCh <- fmt.Errorf("kafka: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		log.Printf("leg failed, shutting down the rest: %v", err)
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	wg.Wait()
}
```

Three blocking calls (`ListenAndServe`, and each `Consumer.Run`), three goroutines, one shared
`context.Context` for shutdown. `kafka.Consumer` and `awssqs.Consumer` are both long-running
pollers, not Lambda/event-source triggers — the right shape for a pod that stays up.

See [Kafka Setup](getting-started-kafka.md) for why one Kafka topic is one Benzene topic here (no
attribute/header indirection the way SQS/HTTP have one) — that's why `"greet"` is both the registered
topic and the literal Kafka topic name.

## 3. Containerise it

One binary, one `Dockerfile`, one image:

```dockerfile
# Dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/app .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/app /app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app"]
```

```bash
docker build -f Dockerfile -t k8s-helloworld:local .
kind load docker-image k8s-helloworld:local
```

## 4. Deploy it

One `Deployment` + `Service` — the SQS and Kafka legs don't get their own, because nothing calls this
pod over either of them; it calls out:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: k8s-helloworld
spec:
  replicas: 2
  selector: { matchLabels: { app: k8s-helloworld } }
  template:
    metadata: { labels: { app: k8s-helloworld } }
    spec:
      containers:
        - name: k8s-helloworld
          image: k8s-helloworld:local
          ports: [{ containerPort: 8080 }]
          env:
            - { name: PORT, value: "8080" }
            - { name: QUEUE_URL, value: "https://sqs.eu-west-1.amazonaws.com/<account-id>/greet" }
            - { name: KAFKA_BROKERS, value: "kafka-bootstrap.kafka.svc.cluster.local:9092" }
          readinessProbe: { tcpSocket: { port: 8080 }, initialDelaySeconds: 3 }
---
apiVersion: v1
kind: Service
metadata:
  name: k8s-helloworld
spec:
  selector: { app: k8s-helloworld }
  ports: [{ port: 80, targetPort: 8080 }]
```

```bash
kubectl apply -f k8s.yaml
kubectl get pods   # 2 pods: 2x k8s-helloworld
```

## 5. Watch the same handler run three ways

```bash
kubectl port-forward service/k8s-helloworld 8080:80 &
curl -XPOST localhost:8080/greet -H 'content-type: application/json' -d '{"name":"world"}'
```

Send a message to the SQS queue or the Kafka topic directly (see [the runnable
example](../examples/k8s-helloworld) for exact commands against a local LocalStack/Kafka pair) and
the **same handler** runs, for a request that never touched HTTP — `kubectl logs
deploy/k8s-helloworld` shows it. That's the proof: one handler, one container, three transports.

```bash
kubectl scale deploy/k8s-helloworld --replicas=4   # scales all three transports' consuming capacity together
```

## Why not just net/http?

Worth asking honestly: `http.HandleFunc("/greet", func(w http.ResponseWriter, r *http.Request) {
...})` does the same job as this guide's five steps in a handful of lines, no `httpbinding` import.
For an HTTP-only service that never talks to anything else, that's a fair trade — the stdlib (or
chi/gorilla if you want a router) already gives HTTP its own routing, and you don't need Benzene to
get it.

The payoff shows up the moment this same handler needs a **second** entry point — a queue another
team publishes to, a Kafka topic, a batch job that used to call this endpoint but really just wants
to drop a message. A bare `http.HandlerFunc` has no answer for that; you'd write a second, separate
handler and keep both in sync by hand. With Benzene the handler above doesn't change at all: a
`kafka.Consumer` or `awssqs.Consumer` goroutine points at the *same* `greetHandler`, because it was
never written against `http.ResponseWriter` in the first place — see section 2 above for that running
as three goroutines in one binary. If HTTP genuinely is and always will be the only way in, reach for
`net/http` (or chi/gorilla) directly instead — you'll write less code, not more.

## One binary, or one per transport?

This guide combines all three transports into a single binary because Go's goroutine model makes it
essentially free to — three blocking loops, three `go func(){...}()` calls, one shared
`context.Context`. It is not the *only* shape, though, and it is not always the right one. Splitting
the transports into **separate** `main` packages/Deployments (one for HTTP, one for the SQS poller,
one for the Kafka consumer, each its own image) is a legitimate alternative: each transport then
scales, rolls back, and fails independently — a bad Kafka-consumer deploy, or the Kafka leg falling
behind under load, no longer risks the HTTP leg's availability the way it does when a crash or a
resource-starved process is shared between all three. The tradeoff is real too: more images to build,
more Deployments to manage, and a little duplicated `newApp()`/wiring per transport.
`greeting/greeting.go` doesn't change either way — only how many binaries and Dockerfiles end up
wrapping it. Reach for separate Deployments when the transports' traffic, failure modes, or scaling
needs genuinely diverge; reach for one binary when they don't and the operational simplicity of a
single image/Deployment is worth more than that independence.

## Next steps

- **More self-hosted workers** — [Kafka Setup](getting-started-kafka.md) covers `kafka.Consumer` in
  depth; `awssqs.Consumer` is documented in its own package doc comment. Both are worth reaching for
  even as a service's *only* transport, since neither's raw client gives you routing or a middleware
  pipeline the way `net/http` gives HTTP.
- **The cloud hosts** — [AWS Lambda](getting-started-aws.md) and
  [Azure Functions](getting-started-azure.md) run the same handler behind a managed event source
  instead of a self-hosted poller.
