# gcpfunctions

The Google Cloud Functions (2nd gen) inbound binding for Benzene: register a Benzene service's
handlers as functions the [Functions
Framework](https://cloud.google.com/functions/docs/functions-framework) serves. It is the Go port
of `GoogleCloud.Functions.Http` (an HTTP-triggered function) and `GoogleCloud.Functions.PubSub` (a
CloudEvent-triggered function — Pub/Sub, Cloud Storage, and any other Eventarc source that delivers
a CloudEvent).

It lives in **its own Go module** because it needs two third-party dependencies:

- `github.com/GoogleCloudPlatform/functions-framework-go` — the Gen2 Go runtime's declarative
  registration API (`functions.HTTP`, `functions.CloudEvent`).
- `github.com/cloudevents/sdk-go/v2` — the CloudEvents SDK, whose `event.Event` is the concrete
  type the framework hands a CloudEvent function.

## Two entry points

### `RegisterHTTP(name, builder, routes)`

Registers an HTTP-triggered function that serves a native
[`httpbinding.Handler`](../httpbinding) — real HTTP status codes and an explicit route table. A
thin pass-through: routing, one DI scope per request, status mapping, and response headers are all
`httpbinding.Handler`'s, unchanged.

```go
package function

import (
	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/gcpfunctions"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"net/http"
)

func init() {
	builder := buildApp() // your *benzene.ApplicationBuilder
	gcpfunctions.RegisterHTTP("Greet", builder, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
	})
}
```

### `RegisterCloudEvent(name, builder, opts...)`

Registers a CloudEvent-triggered function. Each delivered CloudEvent is mapped onto a Benzene
`wire.Request` and dispatched through the pipeline. The mapping mirrors the
[`cloudevents`](../cloudevents) package:

- CloudEvent `type` → topic (when the event carries no type, the topic falls back to the extension
  attribute under the configured reserved topic key — see `WithReservedNames`).
- `data` → body (verbatim JSON).
- `id` / `source` / `subject` / `time` / extension attributes → `ce-`-prefixed headers.

A CloudEvent function **acknowledges by returning `nil` and asks the platform to retry by returning
an error**, so a dispatch whose Benzene result is unsuccessful returns an error — the same
fire-and-forget, let-the-platform-retry posture as `azurefunctions.EventGridHandler`'s outer HTTP
500. Deliveries are at-least-once, so **handlers must be idempotent**.

```go
func init() {
	builder := buildApp()
	gcpfunctions.RegisterCloudEvent("OnOrderCreated", builder,
		gcpfunctions.WithOnFailure(func(ctx context.Context, e cloudevents.Event, resp wire.Response) {
			log.Printf("dispatch of %q failed: %s", e.Type(), resp.StatusCode)
		}),
	)
}
```

Options:

- `WithReservedNames(names)` — override the reserved metadata names (defaults to
  `builder.ReservedNames`). Used for the empty-type topic fallback described above.
- `WithOnFailure(hook)` — called with the event and the `wire.Response` whenever a dispatch is
  unsuccessful (the same events for which the function returns an error). It does not change the
  outcome; it is for logging/metering.

## Deploy

A Gen2 Go function is a package with an `init` that registers the target, deployed with the target
name selected via `FUNCTION_TARGET`. With the [gcloud
CLI](https://cloud.google.com/functions/docs/deploy):

HTTP-triggered:

```
gcloud functions deploy Greet \
  --gen2 --runtime=go125 --region=<region> \
  --source=. --entry-point=Greet \
  --trigger-http --allow-unauthenticated
```

CloudEvent-triggered (Pub/Sub, as one example — any Eventarc source works the same way):

```
gcloud functions deploy OnOrderCreated \
  --gen2 --runtime=go125 --region=<region> \
  --source=. --entry-point=OnOrderCreated \
  --trigger-topic=<pubsub-topic>
```

`--entry-point` is the `name` you passed to `RegisterHTTP` / `RegisterCloudEvent` (it sets
`FUNCTION_TARGET`). The framework owns the server; your package only registers the target.

## What was verified without live GCP

This repo has no live Google Cloud project, so **nothing here was actually deployed**. "Verified"
means:

- `GOWORK=off go build ./...` and `GOWORK=off go vet ./...` compile cleanly against the real,
  fetched Functions Framework and CloudEvents SDK APIs.
- `GOWORK=off go test ./... -race -cover` — 100% of the testable core. The tests build a real
  `event.Event` with the CloudEvents SDK (`event.New()`, `SetType`, `SetData`, …) and drive the
  conversion-and-dispatch core (`dispatchCloudEvent`) directly — success (nil error, no failure
  hook), failure (error returned, `OnFailure` called with the event and response), the empty-type
  reserved-key topic fallback, an unresolvable-topic failure (not a panic), and the topic/body/
  header resolution from an event. No live Functions Framework server and no live GCP are involved.
- The `functions.HTTP` / `functions.CloudEvent` calls in `RegisterHTTP` / `RegisterCloudEvent` are
  the framework's own registration glue (they register a target into the framework's process-global
  registry; the framework, not this package, invokes it at request time). Compile-time `var _`
  assertions pin their signatures so an SDK change breaks the build here rather than at runtime, and
  a smoke test confirms registration does not panic.

The `--runtime=go125` / `--gen2` deploy flags above follow Google's [Go runtime
docs](https://cloud.google.com/functions/docs/concepts/go-runtime) and the module's `go 1.25`
directive; they have not been run against a live project.
