# Getting Started: Benzene on Google Cloud

This guide takes you from a message handler to a Benzene service running on **Google Cloud**, in
idiomatic Go. The handler you write is the same transport-agnostic function every host runs — only
the entry point and the deploy command are Google-specific.

Google Cloud gives Benzene-go two hosting shapes, and this guide walks through both from a runnable
example:

1. **[Cloud Run](#1-cloud-run-the-http-binding-in-a-container)** — a container listening on
   `$PORT`, serving the [`httpbinding`](https://benzene.app/docs/specification/transport-bindings)
   REST handler. No Google-specific Go package is needed: Cloud Run's whole contract is "listen on
   `$PORT`", which `net/http` already satisfies. Follows
   [`examples/gcp-cloudrun-helloworld`](../examples/gcp-cloudrun-helloworld).
2. **[Pub/Sub push](#2-pubsub-push-via-gcppubsub)** — the same handler consuming a Pub/Sub push
   subscription, hosted as a Cloud Run service, via the
   [`gcppubsub`](https://benzene.app/docs/specification/transport-bindings) binding. Follows
   [`examples/gcp-pubsub-helloworld`](../examples/gcp-pubsub-helloworld).

> **Why Cloud Run and not Cloud Functions?** Cloud Functions (2nd gen) is built on Cloud Run under
> the hood. Deploying straight to Cloud Run gets the same scale-to-zero autoscaling with one fewer
> layer, and without the `functions-framework-go` buildpack dependency — the one Google-specific
> dependency this port avoids everywhere else. See the
> [Cloud Run example's README](../examples/gcp-cloudrun-helloworld/README.md#why-cloud-run-and-not-cloud-functions)
> for the full reasoning.

## Prerequisites

- **Read [getting-started.md](getting-started.md) first.** It covers the core concepts this guide
  assumes: [topics, registries, the `Result[T]` handler contract, and the three-phase
  `App` lifecycle](https://benzene.app/docs/specification/core-concepts). Everything below is those
  same pieces with a Google entry point bolted on.
- Go 1.24+.
- The [`gcloud` CLI](https://cloud.google.com/sdk/docs/install), authenticated
  (`gcloud auth login`) with a project set (`gcloud config set project <id>`).
- Docker, and a GCP project with the **Cloud Run** and **Artifact Registry** APIs enabled (plus
  **Pub/Sub** for [part 2](#2-pubsub-push-via-gcppubsub)).

Add the module to your project:

```bash
go get github.com/daniellepelley/benzene-go
```

---

## 1. Cloud Run: the HTTP binding in a container

Cloud Run runs your container and routes HTTP requests to it. Because Benzene's
[`httpbinding.Handler`](https://benzene.app/docs/specification/transport-bindings) is an ordinary
`http.Handler`, there is nothing Google-specific in the code — you write the same service you'd run
anywhere, containerize it, and deploy.

### The handler

The handler knows nothing about Google Cloud. It takes a request, returns a
[`benzene.Result[T]`](https://benzene.app/docs/specification/core-concepts):

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

`benzene.Ok` and `benzene.BadRequest` produce a `Result[T]` carrying a
[Benzene status](https://benzene.app/docs/specification/wire-contracts); the HTTP binding maps that
status to a real HTTP status code (`200`, `400`, …) on the way out.

### Wiring: registry, pipeline, route table

Register the handler against a [topic](https://benzene.app/docs/specification/core-concepts), build
a pipeline, and hand the whole thing to the HTTP binding. This example composes the
`ApplicationBuilder` directly (a small service with no configuration to load):

```go
func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	return httpbinding.Handler(builder, routes)
}
```

`httpbinding.Route` is the explicit route table: it maps an HTTP method + path to a Benzene topic.
`httpbinding.Handler` returns the `http.Handler` that dispatches matching requests through
`RouterMiddleware` to your registered handler.

### The entry point: listen on `$PORT`

Cloud Run's one contract is that your process listens on the port named by the `$PORT` environment
variable. `main` reads it (defaulting to `8080` for local runs) and starts a standard `net/http`
server:

```go
func portFromEnv() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func main() {
	handler := newHandler(newApp())
	port := portFromEnv()
	log.Printf("gcp-cloudrun-helloworld listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
```

### Run it locally

```bash
go run ./examples/gcp-cloudrun-helloworld
# gcp-cloudrun-helloworld listening on :8080

curl -X POST localhost:8080/greet -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST localhost:8080/greet -d '{"name":""}'
# 400 Bad Request
```

### Test it locally

The whole server runs in-process behind `httptest.NewServer` — no Docker, no cloud. Boot the real
`newHandler(newApp())` and push a request through the front door:

```go
func TestGreetEndpoint_ReturnsGreeting(t *testing.T) {
	server := httptest.NewServer(newHandler(newApp()))
	defer server.Close()

	resp, err := http.Post(server.URL+"/greet", "application/json", strings.NewReader(`{"name":"World"}`))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	// ... decode resp.Body into greetResponse and assert "Hello, World!"
}
```

```bash
go test ./examples/gcp-cloudrun-helloworld/...
```

### Containerize and deploy

A small multi-stage build produces a static binary on a distroless base. **The build context must
be the repo root**, because the example imports sibling packages in the module:

```dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./examples/gcp-cloudrun-helloworld

# Cloud Run's only contract is "listen on $PORT" - a minimal static base is enough.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/server /server
ENTRYPOINT ["/server"]
```

Build, push to Artifact Registry, and deploy:

```bash
# From the repo root - the build context needs the whole module.
docker build -f examples/gcp-cloudrun-helloworld/Dockerfile -t gcp-cloudrun-helloworld .

docker tag gcp-cloudrun-helloworld <region>-docker.pkg.dev/<project>/<repo>/gcp-cloudrun-helloworld
docker push <region>-docker.pkg.dev/<project>/<repo>/gcp-cloudrun-helloworld

gcloud run deploy gcp-cloudrun-helloworld \
  --image <region>-docker.pkg.dev/<project>/<repo>/gcp-cloudrun-helloworld \
  --region <region> \
  --allow-unauthenticated
```

When it finishes, `gcloud` prints the service URL. Call it exactly as you did locally:

```bash
SERVICE_URL=$(gcloud run services describe gcp-cloudrun-helloworld --region <region> --format 'value(status.url)')
curl -X POST "$SERVICE_URL/greet" -d '{"name":"World"}'
# {"greeting":"Hello, World!"}
```

> `gcloud run deploy --source .` is a common shortcut, but it uses its source directory as both the
> Docker build context and the place it looks for the Dockerfile. Since this example's Dockerfile
> needs the whole module as context, the explicit build/push/deploy above is what works here.

---

## 2. Pub/Sub push via `gcppubsub`

A [Pub/Sub push subscription](https://cloud.google.com/pubsub/docs/push) delivers each message as an
HTTPS `POST` to your service — no polling, no SDK. The
[`gcppubsub`](https://benzene.app/docs/specification/transport-bindings) binding decodes that push
envelope (base64 data + attributes), resolves the topic per
[wire-contracts §2](https://benzene.app/docs/specification/wire-contracts), and turns the dispatch
result into an acknowledgement: **`204` acks**, **`500` nacks** (and Pub/Sub redelivers per the
subscription's retry policy, dead-lettering if one is configured).

This half is **consumer-only**. Publishing needs the Pub/Sub SDK — a dependency this repo hasn't
taken — so you publish with `gcloud pubsub topics publish`, no code required. The service still runs
on Cloud Run; the difference from [part 1](#1-cloud-run-the-http-binding-in-a-container) is what's
mounted at the endpoint.

### The handler

A push delivery is fire-and-forget: there is no caller to return a body to. The handler does its
work (here, a log line visible in Cloud Logging), and the [result's
status](https://benzene.app/docs/specification/wire-contracts) decides ack vs nack-and-redeliver:

```go
func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	greeting := "Hello, " + req.Name + "!"
	log.Printf("greeted: %s", greeting)
	return benzene.Ok(greetResponse{Greeting: greeting})
}
```

### Wiring: the three-phase `App`

This example uses the full three-phase
[`App` lifecycle](https://benzene.app/docs/specification/core-concepts) — the same composition root
its tests boot from, which is what makes the front-door test below possible:

```go
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}
```

There's no configuration here, so `TConfig` is `struct{}`. `App.Run()` executes the three phases in
order and returns the built `*benzene.ApplicationBuilder`.

### The entry point: mount the push handler on a route

`gcppubsub.Handler(builder)` is an `http.Handler`. Mount it at the path the subscription will point
at — `/pubsub` here — behind a `ServeMux` so nothing else reaches the dispatch pipeline:

```go
func newHandler(builder *benzene.ApplicationBuilder) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/pubsub", gcppubsub.Handler(builder))
	return mux
}

func main() {
	handler := newHandler(newApp().Run())
	port := portFromEnv()
	log.Printf("gcp-pubsub-helloworld listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
```

`portFromEnv` is the same `$PORT` reader as [part 1](#the-entry-point-listen-on-port) — this is a
Cloud Run service too.

### Test it locally

You don't need a real subscription to test the consumer. `benzenetest.NewHost` boots the real `App`,
and `benzenetest.SendPubSub` pushes a native Pub/Sub push delivery in the front door, returning the
HTTP acknowledgement so you can assert ack (`204`) vs nack (`500`):

```go
func TestPushEndpoint_GreetMessageIsAcked(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := benzenetest.SendPubSub(t, host, benzene.NewTopic("greet"), greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusNoContent { // 204 = ack
		t.Errorf("status = %d, want %d (ack)", resp.StatusCode, http.StatusNoContent)
	}
}

func TestPushEndpoint_FailedMessageIsNacked(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := benzenetest.SendPubSub(t, host, benzene.NewTopic("greet"), greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusInternalServerError { // 500 = nack, redeliver
		t.Errorf("status = %d, want %d (nack)", resp.StatusCode, http.StatusInternalServerError)
	}
}
```

```bash
go test ./examples/gcp-pubsub-helloworld/...
```

### Deploy, then create the topic and subscription

The Dockerfile is identical in shape to [part 1](#containerize-and-deploy), only pointed at
`./examples/gcp-pubsub-helloworld`. Build, push, and deploy the Cloud Run service the same way, then
create the Pub/Sub topic and a push subscription that targets the service's `/pubsub` endpoint:

```bash
# From the repo root.
docker build -f examples/gcp-pubsub-helloworld/Dockerfile -t gcp-pubsub-helloworld .
docker tag gcp-pubsub-helloworld <region>-docker.pkg.dev/<project>/<repo>/gcp-pubsub-helloworld
docker push <region>-docker.pkg.dev/<project>/<repo>/gcp-pubsub-helloworld

gcloud run deploy gcp-pubsub-helloworld \
  --image <region>-docker.pkg.dev/<project>/<repo>/gcp-pubsub-helloworld \
  --region <region> \
  --allow-unauthenticated

gcloud pubsub topics create greet-helloworld
SERVICE_URL=$(gcloud run services describe gcp-pubsub-helloworld --region <region> --format 'value(status.url)')
gcloud pubsub subscriptions create greet-helloworld-push \
  --topic greet-helloworld \
  --push-endpoint "$SERVICE_URL/pubsub"
```

> `--allow-unauthenticated` keeps this demo minimal. For anything real, deploy without it and give
> the subscription a push auth service account instead
> (`gcloud pubsub subscriptions create --push-auth-service-account=...`). Pub/Sub then attaches an
> OIDC token that Cloud Run verifies, locking the endpoint to Pub/Sub — no code change here.

### Try it

Publish a message. The `topic` attribute carries the Benzene topic (per
[wire-contracts §2](https://benzene.app/docs/specification/wire-contracts)); the message body is the
handler's request JSON:

```bash
gcloud pubsub topics publish greet-helloworld \
  --message '{"name":"World"}' \
  --attribute topic=greet

# The observable effect is the log line in Cloud Logging:
gcloud run services logs read gcp-pubsub-helloworld --region <region> --limit 5
# ... greeted: Hello, World!
```

A message that fails (`--message '{"name":""}'`) is nacked with a `500` and redelivered per the
subscription's retry policy. Add `--dead-letter-topic` to the subscription to cap the redeliveries.

---

## Next steps

- **Run the same handler over plain HTTP** — [part 1](#1-cloud-run-the-http-binding-in-a-container)
  and [part 2](#2-pubsub-push-via-gcppubsub) share one `greetHandler`; that's the point of the
  [ports-and-adapters model](https://benzene.app/docs/specification/core-concepts).
- **Deploy elsewhere** — the [platform picker in getting-started.md](getting-started.md) links the
  AWS, Azure, and Kubernetes guides. The handler doesn't change.
- **The runnable examples** — [`examples/gcp-cloudrun-helloworld`](../examples/gcp-cloudrun-helloworld)
  and [`examples/gcp-pubsub-helloworld`](../examples/gcp-pubsub-helloworld), each with a README
  documenting the deploy workflow and exactly what was verified in CI.
