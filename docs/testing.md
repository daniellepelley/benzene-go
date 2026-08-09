# Testing

A Benzene handler is a plain function and the pipeline that hosts it is plain Go, so testing a
Benzene service needs nothing beyond the standard `testing` package. This guide covers the two
levels you'll test at:

1. **A handler in isolation** — call the function directly and assert on the
   [`benzene.Result[T]`](https://benzene.app/docs/specification/core-concepts.html) it returns.
2. **The whole pipeline in-memory** — boot the real app and push native events through the
   `benzenetest` harness, exercising middleware, routing, dependency resolution, and the transport
   binding without a network listener.

The [getting-started guide §7](getting-started.md) showed the first `benzenetest` test; this guide
enumerates the full harness and the patterns the runnable examples use.

## The core principle: test the wiring that ships

Both `main()` and your tests boot from the **same composition root** — the `newApp()` function that
returns a `benzene.App[TConfig]`. `main()` calls `newApp().Run()`; a test hands the same `newApp()`
to `benzenetest.NewHost`. Because `NewHost` runs the identical three-phase lifecycle
(`GetConfiguration` → `ConfigureServices` → `Configure`, see
[Core concepts §7](https://benzene.app/docs/specification/core-concepts.html)), a test exercises
exactly the registry, container, and pipeline that deploy — not a re-declared approximation of them.
The only test-time seam is `WithServices`, which swaps an external edge for a fake *before* the
pipeline is built (more below).

## Level 1: call the handler directly

A handler is a `benzene.Handler[TReq, TRes]` — `func(context.Context, TReq) benzene.Result[TRes]`.
Nothing stops you calling it like any other function and asserting on the `Result`. This is the
fastest, most focused test for pure request → result logic, from
[`examples/aws-sqs-helloworld/greeting/greeting_test.go`](https://github.com/daniellepelley/benzene-go/blob/main/examples/aws-sqs-helloworld/greeting/greeting_test.go):

```go
func TestHandler_ReturnsGreeting(t *testing.T) {
	result := Handler(context.Background(), GreetRequest{Name: "World"})

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload == nil || result.Payload.Greeting != "Hello, World!" {
		t.Errorf("Payload = %+v, want Greeting=Hello, World!", result.Payload)
	}
}

func TestHandler_MissingNameIsBadRequest(t *testing.T) {
	result := Handler(context.Background(), GreetRequest{Name: ""})

	if result.Status != benzene.StatusBadRequest {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusBadRequest)
	}
}
```

`Result[T]` is a value with three fields you assert on:

- **`Status`** — a `benzene.Status` from the framework vocabulary (`benzene.StatusOk`,
  `benzene.StatusBadRequest`, …), the [status table](https://benzene.app/docs/specification/wire-contracts.html)
  each transport maps to its own failure signal.
- **`Payload`** — a `*T`, non-nil on success (a pointer so "absent" is distinct from `T`'s zero
  value). Nil-check it before dereferencing, as the example does.
- **`Errors`** — a `[]string` of human-readable messages, populated on failure.

### What a direct call does *not* cover

Calling the function bypasses everything the pipeline does around it — routing, middleware, and
crucially the **DI scope** that `RouterMiddleware`/`envelope.Dispatch` attach to the context. A
handler that resolves a dependency with `benzene.ScopeFromContext(ctx)` will not find one when
called with a bare `context.Background()`. The helloworld example turns that into a deliberate
defensive test rather than a gap, in
[`examples/helloworld/main_test.go`](https://github.com/daniellepelley/benzene-go/blob/main/examples/helloworld/main_test.go):

```go
func TestGreetHandler_NoScopeOnContextIsUnexpectedError(t *testing.T) {
	// A direct unit-level call bypassing the HTTP wiring, which always attaches a scope
	// (RouterMiddleware/envelope.Dispatch) - this defends against greetHandler ever being
	// invoked outside that wiring.
	result := greetHandler(context.Background(), greetRequest{Name: "World"})

	if result.Status != benzene.StatusUnexpectedError {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusUnexpectedError)
	}
}
```

When a handler depends on the scope or on the pipeline's behaviour, reach for the in-memory host.

## Level 2: drive the pipeline with `benzenetest`

The `benzenetest` package is an in-process test host: your real app booted from its composition
root, with a per-transport `Send*` helper that turns a Benzene-level `(topic, payload, headers)`
into that transport's native event at the front door and the native response back out. No HTTP
listener, no cloud SDK, no credentials.

### Constructing the host

`benzenetest.NewHost(app, opts...)` runs the app's lifecycle and returns a `*benzenetest.Host`:

```go
host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))
```

Two `Option`s configure it, both applied before `Configure` builds the pipeline:

- **`benzenetest.WithRoutes(routes ...httpbinding.Route)`** — supplies the HTTP route table
  (path/method → topic) the HTTP-shaped hosts need. It's the one piece of wiring an app declares
  next to its HTTP entry point in `main()` rather than in the pipeline, so a test declares the same
  `routes()` here. Queue-shaped transports (SQS, SNS, Pub/Sub) route by message attribute and ignore
  it — those tests can call `NewHost(newApp())` with no routes.
- **`benzenetest.WithServices(action func(builder *benzene.ApplicationBuilder))`** — runs after the
  app's own `ConfigureServices` but before `Configure`, so a registration here is last-wins over the
  app's and every handler resolves the fake. This is how you swap the outbound client, a store, or a
  clock for a test double (see [Faking the outbound edge](#faking-the-outbound-edge)).

The `Host` also exposes `host.Builder()` (the built `*benzene.ApplicationBuilder`) and
`host.Routes()`; the `Send*` helpers read these for you, so tests rarely touch them directly.

### The send helpers

A single `Send*` call specializes the host to a transport, dispatches one event, and decodes the
native response. Lines that create the host and assert are identical across transports — **only the
`Send*` call changes**. That parallelism is the point.

| Helper | Package | Front door | Returns |
| --- | --- | --- | --- |
| `SendHTTP(t, host, method, path, payload, headers)` | `benzenetest` | native `net/http` | `HTTPResponse` |
| `SendEnvelope(t, host, topic, payload, headers)` | `benzenetest` | raw wire envelope (service-to-service) | `wire.Response` |
| `SendAPIGateway(t, host, method, path, payload, headers)` | `benzenetest` | AWS API Gateway v2 / Function URL | `HTTPResponse` |
| `SendPubSub(t, host, topic, payload, headers)` | `benzenetest` | GCP Pub/Sub push | `HTTPResponse` (204 ack / 500 nack) |
| `SendAzureHTTP(t, host, method, path, payload, headers)` | `benzenetest` | Azure Functions HTTP trigger | `HTTPResponse` |
| `SendAzureQueue(t, host, dataName, path, topic, payload, headers)` | `benzenetest` | Azure Functions queue trigger | `HTTPResponse` (200 / 500) |
| `SendCosmosChangeFeed(t, host, dataName, path, topic, documents)` | `benzenetest` | Azure Cosmos DB change feed | `HTTPResponse` (200 / 500) |
| `SendDynamoDBStream(t, host, eventName, tableName, sequenceNumber, newImage)` | `benzenetest` | Lambda DynamoDB stream | `[]string` (failing sequence numbers) |
| `SendSQS(t, host, topic, payload, headers)` | `awssqs` | Lambda SQS event-source mapping | `awssqs.SQSResponse` |
| `SendSNS(t, host, topic, payload, headers)` | `awssns` | Lambda SNS notification | `error` (SNS has no partial-failure report) |

The AWS SQS/SNS helpers live in their own modules (`awssqs`, `awssns`) so those packages don't pull
`testing` into a production build; they take a `benzenetest.TB` and `*benzenetest.Host` exactly like
the in-package helpers. `SendSQS`/`SendSNS` stamp a fixed message id on the single record they
deliver — `awssqs.TestMessageID` / `awssns.TestMessageID` — so a test can assert a batch-item
failure names it.

### The response shapes

**`HTTPResponse`** (every HTTP-shaped `Send*`) carries the framework-mapped HTTP result:

```go
type HTTPResponse struct {
	StatusCode int               // real HTTP code, mapped from the Benzene status via httpstatus
	Headers    map[string]string // response headers, keys lower-cased
	Body       string            // the response body
}
```

**`wire.Response`** (`SendEnvelope`) carries the Benzene status *itself*, not an HTTP code — the
envelope front door is always transport-level OK and the real outcome is inside:

```go
type wire.Response struct {
	StatusCode string            // a Benzene status vocabulary value, e.g. "ok"
	Headers    map[string]string
	Body       string
}
```

### Asserting on status, body, and headers

Drive the native-HTTP front door, check the mapped status code, then JSON-unmarshal the body — the
shape every HTTP example uses, from
[`examples/helloworld/main_test.go`](https://github.com/daniellepelley/benzene-go/blob/main/examples/helloworld/main_test.go):

```go
func TestGreetEndpoint_ReturnsGreeting(t *testing.T) {
	host := benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
	}

	var got greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", got.Greeting, "Hello, World!")
	}
}
```

`payload` is serialized for you: a `string`, `[]byte`, or `json.RawMessage` is sent verbatim,
anything else is JSON-marshalled — so passing the handler's own request struct (`greetRequest{...}`)
is the idiom. A failing status maps straight to its HTTP code, so a missing name is a plain
`http.StatusBadRequest` assertion:

```go
resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: ""}, nil)
if resp.StatusCode != http.StatusBadRequest {
	t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
}
```

**Headers.** `HTTPResponse.Headers` are the mapped response headers with **lower-cased keys**. The
health-check test reads the JSON body of a well-known route; the same unmarshal pattern applies to
any structured body:

```go
resp := benzenetest.SendHTTP(t, host, http.MethodGet, httpbinding.HealthPath, nil, nil)
if resp.StatusCode != http.StatusOK {
	t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
}
var body struct {
	IsHealthy bool `json:"isHealthy"`
}
if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
	t.Fatalf("json.Unmarshal() error = %v; body = %s", err, resp.Body)
}
```

Request headers ride into a `SendHTTP` call as the final `headers map[string]string` argument (pass
`nil` for none).

### The envelope front door

`SendEnvelope` drives the raw [wire-contracts](https://benzene.app/docs/specification/wire-contracts.html)
envelope — the service-to-service path where there's no route table. Its `wire.Response.StatusCode`
is the Benzene status string, so compare against `string(benzene.StatusOk)`:

```go
func TestInvokeEndpoint_EnvelopeRoundTrip(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := benzenetest.SendEnvelope(t, host, benzene.NewTopic("greet"), greetRequest{Name: "Envelope"}, nil)

	if resp.StatusCode != string(benzene.StatusOk) {
		t.Fatalf("envelope statusCode = %q, want %q; body = %s", resp.StatusCode, benzene.StatusOk, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body = %s", err, resp.Body)
	}
	if greeting.Greeting != "Hello, Envelope!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, Envelope!")
	}
}
```

### Queue-shaped transports report batches, not bodies

A queue consumer has no HTTP response — success or redelivery is the observable outcome. `SendSQS`
returns an `awssqs.SQSResponse` whose `BatchItemFailures` is empty on success and names the failing
record otherwise, from
[`examples/aws-sqs-helloworld/consumer/main_test.go`](https://github.com/daniellepelley/benzene-go/blob/main/examples/aws-sqs-helloworld/consumer/main_test.go):

```go
func TestConsumer_ValidGreetMessageSucceeds(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := awssqs.SendSQS(t, host, benzene.NewTopic("greet"), greeting.GreetRequest{Name: "World"}, nil)

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}
}

func TestConsumer_MissingNameIsReportedAsBatchItemFailure(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := awssqs.SendSQS(t, host, benzene.NewTopic("greet"), greeting.GreetRequest{Name: ""}, nil)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != awssqs.TestMessageID {
		t.Errorf("BatchItemFailures = %v, want [{%s}]", resp.BatchItemFailures, awssqs.TestMessageID)
	}
}
```

`SendDynamoDBStream` returns the failing records' sequence numbers as a `[]string`; the Pub/Sub and
Azure queue helpers surface ack/nack as an `HTTPResponse.StatusCode` (204/200 vs 500).

## Faking the outbound edge

For a service that publishes downstream — the ingress → handler → **egress** shape — swap the real
outbound client for `benzenetest.FakeMessageSender` via `WithServices`, then assert on what it
captured. `FakeMessageSender` satisfies `client.Sender`, records the last call, and reports every
send as `benzene.Accepted` by default:

| Member | Purpose |
| --- | --- |
| `benzenetest.NewFakeMessageSender()` | construct one (default result = `Accepted`) |
| `.WithResult(result benzene.Result[json.RawMessage])` | set the result every `Send` returns, for failure-path tests |
| `.Calls() int` | number of `Send` calls recorded |
| `.LastTopic() benzene.Topic` | topic of the most recent send |
| `.LastMessage() []byte` | raw body of the most recent send |
| `.LastHeaders() map[string]string` | headers of the most recent send |
| `.DecodeLastMessage(t, v any)` | JSON-unmarshal the last body into `v`, failing the test on error |

Register it in `WithServices` and assert on both the native response and the captured egress, from
[`examples/aws-sqs-helloworld/publisher/main_test.go`](https://github.com/daniellepelley/benzene-go/blob/main/examples/aws-sqs-helloworld/publisher/main_test.go):

```go
func newTestHost(t *testing.T, fake *benzenetest.FakeMessageSender) *benzenetest.Host {
	t.Helper()
	return benzenetest.NewHost(newApp(wiredButOverridden(t)),
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			client.RegisterSender(b.Container, fake)
		}),
		benzenetest.WithRoutes(routes()...),
	)
}

func TestPublisher_ForwardsToTopicAndReturnsAccepted(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender()
	host := newTestHost(t, fake)

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greeting.GreetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("statusCode = %d, want %d; body = %s", resp.StatusCode, http.StatusAccepted, resp.Body)
	}
	if got := fake.LastTopic(); got != benzene.NewTopic("greet") {
		t.Errorf("LastTopic = %v, want greet", got)
	}
	var sent greeting.GreetRequest
	fake.DecodeLastMessage(t, &sent)
	if sent.Name != "World" {
		t.Errorf("published Name = %q, want %q", sent.Name, "World")
	}
}
```

For the publish-failure path, seed the fake with a failing result and assert the handler surfaces it:

```go
fake := benzenetest.NewFakeMessageSender().WithResult(benzene.ServiceUnavailable[json.RawMessage]("boom"))
host := newTestHost(t, fake)

resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greeting.GreetRequest{Name: "World"}, nil)
if resp.StatusCode != http.StatusServiceUnavailable {
	t.Errorf("statusCode = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
}
```

## One test, any transport

Because the host setup and assertions don't name a transport, moving a test from local HTTP to a
cloud host — or from one cloud to another — changes only the `Send*` line. A greet handler tested
with `SendHTTP` runs unchanged behind `SendAPIGateway` on AWS or `SendPubSub` on GCP; the consumer
tested with `SendSQS` runs behind `SendPubSub` with the same host and the same assertions on the
outcome. This mirrors the deployment story from [getting-started §7](getting-started.md): the handler
is the asset, the host is a detail.

## Lower-level: `Invoke`

When you want to drive the pipeline directly against a builder — no transport binding at all — use
`benzenetest.Invoke`. It runs one invocation and returns the typed `Result[TRes]`, with the
handler's payload type-asserted into `TRes`:

```go
func Invoke[TReq, TRes any](
	ctx context.Context,
	builder *benzene.ApplicationBuilder,
	topic benzene.Topic,
	headers map[string]string,
	request TReq,
) benzene.Result[TRes]
```

```go
result := benzenetest.Invoke[greetRequest, greetResponse](
	context.Background(), builder, benzene.NewTopic("greet"), nil, greetRequest{Name: "World"})

if result.Status != benzene.StatusOk {
	t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
}
```

`Invoke` creates a fresh scope, so unlike a direct function call it *does* give the handler a DI
scope on the context. Every outcome becomes a `Result` — a pipeline error returns
`ServiceUnavailable`, an empty pipeline returns `UnexpectedError` — so a test always has a `Result`
to assert on rather than a raw Go error. Pass `host.Builder()` to run it against a full app, or a
hand-built `*benzene.ApplicationBuilder` for a narrower pipeline. Prefer a `Send*` helper when you
want to exercise a specific transport's mapping; reach for `Invoke` when the transport is irrelevant
to what you're testing.

## Building native events by hand

Each `Send*` helper is a thin wrapper over a `New*Event` builder (`NewHTTPRequest`,
`NewAPIGatewayEvent`, `NewEnvelopeEvent`, `NewSQSEvent`, `NewSNSEvent`, `NewPubSubEvent`,
`NewAzureHTTPEvent`, `NewCosmosChangeFeedEvent`, `NewDynamoDBStreamEvent`) plus a dispatch and
decode. These are exported for the occasional hand-rolled dispatch — a malformed-event or
partial-batch case a `Send*` helper doesn't parameterize — but the `Send*` helpers cover the common
path and are what the examples use.

## See also

- [Getting started](getting-started.md) — the composition root and the first `benzenetest` test.
- [Core concepts](https://benzene.app/docs/specification/core-concepts.html) — the App lifecycle,
  the pipeline, and `Result[T]`.
- [Wire contracts](https://benzene.app/docs/specification/wire-contracts.html) — the status
  vocabulary and the envelope `SendEnvelope` exercises.
- [`examples/`](https://github.com/daniellepelley/benzene-go/tree/main/examples) — every runnable
  service and its tests.
</content>
</invoke>
