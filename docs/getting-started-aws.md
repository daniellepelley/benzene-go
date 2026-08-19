# Getting Started: Benzene on AWS Lambda

Benzene runs on AWS Lambda as a single custom-runtime binary that answers several event sources
through one middleware pipeline. The same handler you write is reached over HTTP (a Lambda Function
URL or API Gateway), a direct envelope invoke, SQS, SNS, DynamoDB Streams, and EventBridge — adding
a transport is one line of wiring in `main`, never a change to your handler.

This guide starts from the runnable
[`examples/aws-lambda-helloworld`](../examples/aws-lambda-helloworld) and ends with a Lambda deployed
as a `bootstrap` binary on the `provided.al2023` runtime, fronted by a Function URL (no API Gateway
resource required), then adds the event-source transports Go supports.

> **Runnable version:** everything below is in
> [`examples/aws-lambda-helloworld`](../examples/aws-lambda-helloworld) — `main.go` (the handler,
> composition root, and Lambda wiring), `main_test.go` (in-memory tests, no AWS account needed),
> `Dockerfile` and `template.yaml` (the deploy). Read it alongside this page.

New to Benzene's concepts (topics, handlers, `Result`, the three-phase `App`)? Start with
[Getting Started](./getting-started.md) first — this page assumes them and only covers the AWS
specifics. Deploying elsewhere? See [Azure Functions](./getting-started-azure.md) and
[Google Cloud](./getting-started-google.md).

## Prerequisites

- [Go 1.24+](https://go.dev/dl/)
- To deploy: [Docker](https://docs.docker.com/get-docker/), the
  [AWS CLI](https://aws.amazon.com/cli/) configured with credentials, and the
  [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html).
  This example ships as a **container image** (Lambda has no HTTP-server contract for Go — only the
  Runtime API — so Benzene ships a hand-rolled runtime loop in a `bootstrap` binary), so Docker is
  needed even for a Function-URL-only deploy.

## 1. Create the project

```bash
mkdir myfunction && cd myfunction
go mod init github.com/you/myfunction
go get github.com/daniellepelley/benzene-go
```

The root module (`benzene-go`) carries the pipeline, the HTTP binding, the `awslambda` runtime, and
the zero-dependency event sources (`awsdynamodb`). The transports that need the AWS SDK — `awssqs`,
`awssns`, `awseventbridge` — are separate modules; `go get` them only when you wire one up (see
[Supported event sources](#supported-event-sources)).

## 2. Define a message handler

Business logic lives in a handler — a plain function of `(context.Context, TRequest) → benzene.Result[TResponse]`
— not in the Lambda entry point, so it stays testable and portable across hosts. See
[Message Handlers](https://benzene.app/docs/specification/core-concepts) for the full picture; the
minimal shape (from `main.go`) is:

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

The handler returns a `benzene.Result[T]` carrying a Benzene status (`Ok`, `BadRequest`, …). Each
status maps to a real HTTP code on HTTP transports and to a success/failure decision on the event
sources — the same result, interpreted per transport, so the handler never knows which front door it
was reached through.

## 3. Wire the application (the composition root)

Benzene's three-phase `App` lifecycle (`GetConfiguration` → `ConfigureServices` → `Configure`) is
the platform-neutral application definition every host shares. `newApp` is the single composition
root both `main` and the tests boot from — so a test exercises exactly the wiring that ships:

```go
// newApp is the composition root both main() and the tests boot from.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
		},
	}
}
```

`ConfigureServices` registers your handlers (and any dependencies) against the `Registry` and DI
`Container`; `Configure` builds the pipeline — here just `RouterMiddleware`, which dispatches each
invocation to the handler registered for its topic. `App.Run()` executes these phases once and
returns a built `*benzene.ApplicationBuilder` (registry + container + pipeline) that transport
bindings attach entry points to. This is the same `App` you'd hand to `httpbinding` for a plain HTTP
server; only the entry point in `main` changes for Lambda.

The HTTP route table lives next to the entry point, not in the pipeline — one `POST /greet` route
mapped to the `greet` topic:

```go
// routes is the HTTP route table both main() and the tests use.
func routes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
}
```

## 4. Wire the Lambda entry point

A custom-runtime Lambda doesn't listen on a port — the execution environment repeatedly polls the
[Lambda Runtime API](https://docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html) for the next
invocation event and posts back a response. The `awslambda` package implements that loop
(`awslambda.Start`) and adapts a built `ApplicationBuilder` into the two event shapes a Benzene
Lambda accepts:

- **`awslambda.HTTPHandler(builder, routes)`** — for a Lambda fronted by any of AWS's HTTP front
  doors: a Function URL / API Gateway HTTP API (v2.0 payload), an API Gateway REST API / HTTP API
  v1.0 payload, or an ALB target group. The shape is detected per invocation; the response uses the
  matching shape and carries real HTTP status codes.
- **`awslambda.EnvelopeHandler(builder)`** — for a direct/Lambda-to-Lambda invoke carrying the raw
  [wire envelope](https://benzene.app/docs/specification/wire-contracts) (`{"topic": …, "headers": …, "body": …}`)
  with no HTTP layer.

Both return an `awslambda.HandlerFunc` (`func(context.Context, json.RawMessage) (json.RawMessage, error)`).
The example combines them so one Lambda answers both callers — dispatching on whether the event has
a `requestContext` (present on Function URL / API Gateway events, absent on a bare envelope):

```go
// newHandler dispatches to awslambda.HTTPHandler for a Function-URL-shaped event (has a
// "requestContext") and awslambda.EnvelopeHandler otherwise - so this one Lambda answers both
// an HTTP caller (curl against the Function URL) and a direct/Lambda-to-Lambda envelope invoke.
func newHandler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	httpHandler := awslambda.HTTPHandler(builder, routes())
	envelopeHandler := awslambda.EnvelopeHandler(builder)

	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var probe struct {
			RequestContext json.RawMessage `json:"requestContext"`
		}
		if err := json.Unmarshal(event, &probe); err == nil && len(probe.RequestContext) > 0 {
			return httpHandler(ctx, event)
		}
		return envelopeHandler(ctx, event)
	}
}

func main() {
	awslambda.Start(newHandler(newApp().Run()))
}
```

That `newHandler` dispatch is application glue, not a Benzene feature — if you only need one front
door, hand `awslambda.HTTPHandler(builder, routes())` (or `EnvelopeHandler(builder)`, or an event
source's `Handler(builder)` from the sections below) straight to `awslambda.Start`. The SQS, SNS, and
DynamoDB examples each do exactly that.

`awslambda.Start` reads `AWS_LAMBDA_RUNTIME_API` (set automatically by the execution environment),
runs the poll → invoke → respond loop forever, and recovers a handler panic into a reported error
rather than crashing the process. It's meant to run only inside a deployed Lambda; tests never call
it (see the next section).

## 5. Test locally

The `benzenetest` package boots your real `newApp` from its composition root and pushes native
events through the front door — no AWS account, no network, no `Start` loop. From `main_test.go`:

```go
func newTestHost() *benzenetest.Host {
	return benzenetest.NewHost(newApp(), benzenetest.WithRoutes(routes()...))
}

func TestNewHandler_FunctionURLEventReturnsGreeting(t *testing.T) {
	resp := benzenetest.SendAPIGateway(t, newTestHost(), http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	// ... unmarshal resp.Body and assert the greeting
}

func TestNewHandler_EnvelopeEventRoundTrip(t *testing.T) {
	resp := benzenetest.SendEnvelope(t, newTestHost(), benzene.NewTopic("greet"), greetRequest{Name: "Envelope"}, nil)

	if resp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("StatusCode = %q, want %q; body = %s", resp.StatusCode, benzene.StatusOk, resp.Body)
	}
}
```

`benzenetest.NewHost(newApp(), …)` runs the same `GetConfiguration`/`ConfigureServices`/`Configure`
that a real deploy runs. Each transport has a parallel `Send*` helper that builds that transport's
native event and returns its native response:

- `benzenetest.SendAPIGateway` — an API Gateway v2 / Function URL request (returns an `HTTPResponse`
  with the real HTTP status code)
- `benzenetest.SendEnvelope` — the raw wire envelope (returns a `wire.Response` carrying the Benzene
  status)
- `benzenetest.WithServices(...)` — swap a real dependency (an outbound client, a store, a clock)
  for a fake before `Configure` builds the pipeline, the standard way to isolate a handler in a test

The event-source `Send*` helpers (`awssqs.SendSQS`, `awssns.SendSNS`,
`benzenetest.SendDynamoDBStream`) follow the same shape — see each section below.

```bash
go test ./...
```

## 6. Build and deploy

The example deploys as a **container image**: a static `bootstrap` binary cross-compiled for Linux,
copied onto AWS's `provided.al2023` base image. The `Dockerfile`:

```dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /out/bootstrap ./examples/aws-lambda-helloworld

# AWS's own base image for a custom (provided.al2023) runtime - sets LAMBDA_TASK_ROOT and
# provides the runtime interface client that invokes ${LAMBDA_TASK_ROOT}/bootstrap.
FROM public.ecr.aws/lambda/provided:al2023
COPY --from=build /out/bootstrap ${LAMBDA_TASK_ROOT}/bootstrap
CMD [ "bootstrap" ]
```

The binary **must** be named `bootstrap` — that's the entry point the `provided.al2023` runtime
invokes. `CGO_ENABLED=0` produces a static binary; `GOARCH=arm64` matches the `Architectures: [arm64]`
in the template (use `amd64` in both places if you prefer x86). Note the build context is the repo
root, because the example imports sibling packages in the module.

The SAM `template.yaml` declares a container-image function with a public Function URL — no API
Gateway resource:

```yaml
AWSTemplateFormatVersion: '2010-09-09'
Transform: AWS::Serverless-2016-10-31

Globals:
  Function:
    Timeout: 5
    MemorySize: 128

Resources:
  HelloWorldFunction:
    Type: AWS::Serverless::Function
    Properties:
      PackageType: Image
      Architectures:
        - arm64
      FunctionUrlConfig:
        AuthType: NONE
    Metadata:
      DockerTag: aws-lambda-helloworld
      DockerContext: ../..
      Dockerfile: examples/aws-lambda-helloworld/Dockerfile

Outputs:
  FunctionUrl:
    Description: The Function URL to curl
    Value: !GetAtt HelloWorldFunctionUrl.FunctionUrl
```

Then build and deploy:

```bash
sam build
sam deploy --guided
```

`sam deploy --guided` walks you through stack name, region, and confirms creating the public
(`AuthType: NONE`) Function URL on first run, then remembers your answers in `samconfig.toml`. The
output prints the Function URL. Try it:

```bash
curl -X POST "$FUNCTION_URL/greet" -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST "$FUNCTION_URL/greet" -d '{"name":""}'
# 400 Bad Request
```

> `FunctionUrlConfig: AuthType: NONE` makes the endpoint public. Use `AWS_IAM` and sign requests, or
> front the function with API Gateway plus an authorizer, for anything non-public.

## Supported event sources

A Benzene Lambda routes any of these to the same handlers by topic. Each is a different `Handler`
constructor you hand to `awslambda.Start` (or dispatch to from a combined `newHandler` like the one
above). Go currently supports the four below.

| Source | Package | Entry point | Module |
|---|---|---|---|
| HTTP (Function URL / API Gateway / ALB) | `awslambda` | `awslambda.HTTPHandler(builder, routes)` | root |
| Direct invoke (wire envelope) | `awslambda` | `awslambda.EnvelopeHandler(builder)` | root |
| SQS | `awssqs` | `awssqs.Handler(builder)` | own module (AWS SDK) |
| SNS | `awssns` | `awssns.Handler(builder)` | own module (AWS SDK) |
| DynamoDB Streams | `awsdynamodb` | `awsdynamodb.Handler(builder)` | root (zero deps) |
| EventBridge | `awseventbridge` | `awseventbridge.Handler(builder)` | own module (AWS SDK) |

The topic-resolution and failure semantics of each source follow the language-neutral
[transport bindings](https://benzene.app/docs/specification/transport-bindings) spec.

### SQS

A Lambda triggered by an SQS event source mapping. `awssqs.Handler(builder)` is the whole entry
point:

```go
func main() {
	awslambda.Start(awssqs.Handler(newApp().Run()))
}
```

Each record in the batch gets its own pipeline invocation and DI scope. The topic is resolved per
[wire-contracts §2](https://benzene.app/docs/specification/wire-contracts): from a `topic` message
attribute when present, otherwise the record body is parsed as a full wire envelope. A record whose
dispatch result is not a success status is reported back as a **partial batch failure** (its
`messageId` in `batchItemFailures`), so only failed records are redelivered — this requires
`FunctionResponseTypes: [ReportBatchItemFailures]` on the event source mapping. The `awssqs` module
also has an outbound `awssqs.NewClient(...)` for publishing (via the SDK's `SendMessage`); the
[`examples/aws-sqs-helloworld`](../examples/aws-sqs-helloworld) example pairs a publisher Lambda
(Function URL, forwarding via that client) with this consumer.

Test it with `awssqs.SendSQS`:

```go
resp := awssqs.SendSQS(t, host, benzene.NewTopic("greet"), greeting.GreetRequest{Name: "World"}, nil)
// resp.BatchItemFailures is empty on success, or names awssqs.TestMessageID on failure
```

### SNS

A Lambda subscribed directly to an SNS topic. `awssns.Handler(builder)`:

```go
func main() {
	awslambda.Start(awssns.Handler(newApp().Run()))
}
```

A direct SNS-to-Lambda subscription has no batch or partial-failure concept — each notification is
its own asynchronous invocation. Topic resolution is the same as SQS (a `topic` message attribute,
else an envelope in the message body). On a non-success result, `awssns.Handler` returns a **Go
error**, which triggers AWS's own async-invoke retry (and a dead-letter queue if configured) — there
is no partial-failure response to report to. The `awssns` module has an outbound `Client` (via
`Publish`); [`examples/aws-sns-helloworld`](../examples/aws-sns-helloworld) shows the full
publisher + consumer pair. Test with `awssns.SendSNS` (which returns an `error`, nil on success).

### DynamoDB Streams

A Lambda triggered by a DynamoDB table's stream — change-data-capture for table
inserts/modifies/removes. Zero-dependency (the stream delivers records as plain JSON), so it lives in
the root module. `awsdynamodb.Handler(builder)`:

```go
func main() {
	awslambda.Start(awsdynamodb.Handler(newApp().Run()))
}
```

The topic is `"{tableName}:{eventName}"` — the table parsed from the stream ARN plus the change type
— so a handler registers `benzene.NewTopic("orders:INSERT")`. The body is the record's image
unmarshalled from DynamoDB's AttributeValue format into plain JSON (`NewImage`, else `OldImage` for
REMOVE, else `Keys`), so your handler deserializes an ordinary struct and never sees `{"S":…}` /
`{"N":…}` wrappers. Envelope metadata is exposed as `dynamodb-`-prefixed headers
(`dynamodb-event-name`, `dynamodb-table`, `dynamodb-sequence-number`, …).

Because stream records within a shard are **ordered**, the batch is processed sequentially and
**stops at the first failure**: that record's `SequenceNumber` is returned as the sole partial batch
failure, so Lambda checkpoints there and redelivers from that record (again requiring
`ReportBatchItemFailures`). This is deliberately different from SQS's concurrent fan-out. There is no
publisher side — writing to the table is the publish. See
[`examples/aws-dynamodb-helloworld`](../examples/aws-dynamodb-helloworld). Test with
`benzenetest.SendDynamoDBStream`:

```go
failures := benzenetest.SendDynamoDBStream(t, host, "INSERT", "orders", "seq-1", order{ID: "1", Item: "book"})
// failures is empty on success, or holds the sequence number on failure
```

### EventBridge

A Lambda invoked by an EventBridge rule. Its own module (the outbound client needs the AWS SDK).
`awseventbridge.Handler(builder)`:

```go
func main() {
	awslambda.Start(awseventbridge.Handler(newApp().Run()))
}
```

The event's `detail-type` is the topic verbatim — EventBridge's own native routing key, so
`benzene.NewTopic("order.created")` handles events published with that detail-type — and `detail` is
the body. Envelope metadata (`id`, `source`, `account`, `region`, `time`, `detail-type`) is exposed
as `eventbridge-`-prefixed headers, and Benzene wire headers embedded by an outbound EventBridge
client are lifted from the reserved `_benzeneHeaders` key inside `detail`. Like SNS, delivery is
fire-and-forget and one event per invocation; a non-success result returns a Go error, triggering
AWS's async-invoke retry. The `awseventbridge` module also has an outbound `Client` (via `PutEvents`).

### Not yet in the Go port

The .NET guide additionally lists **S3**, **Kinesis Data Streams**, and **Kafka-on-Lambda** (MSK /
self-managed) as event sources. The Go port does **not** have these Lambda bindings yet — there is no
`awss3`, `awskinesis`, or Kafka-on-Lambda package. (A standalone `kafka` module exists for the
consumer-group/broker path, but not the Lambda event-source-mapping shape.) See
[`ROADMAP.md`](../ROADMAP.md) for what's planned.

## IAM

The IAM requirements differ per source, and the example templates encode the minimum for each:

- **HTTP (Function URL)** — no extra execution-role permissions to receive; `AuthType: NONE` (public)
  or `AWS_IAM` (signed) governs access, not the execution role.
- **SQS** — the consumer's execution role needs the queue-poll permissions (`sqs:ReceiveMessage`,
  `sqs:DeleteMessage`, `sqs:GetQueueAttributes`); the SAM `SQSPollerPolicy` grants them. A publisher
  needs `sqs:SendMessage` (`SQSSendMessagePolicy`). See
  [`examples/aws-sqs-helloworld/template.yaml`](../examples/aws-sqs-helloworld/template.yaml).
- **SNS** — SNS invokes the function via a resource-based Lambda permission (SAM's `Type: SNS` event
  wires it), so no extra execution-role IAM is needed to receive. A publisher needs `sns:Publish`
  (`SNSPublishMessagePolicy`).
- **DynamoDB Streams** — the execution role needs the stream-read permissions
  (`dynamodb:GetRecords`, `GetShardIterator`, `DescribeStream`, `ListStreams`); SAM's `Type: DynamoDB`
  event wires the mapping and role.
- **EventBridge** — invokes via a resource-based Lambda permission (the rule's target), so no extra
  execution-role IAM to receive.

## Observability

- **Tracing & metrics** — the [`diagnostics`](../diagnostics) module (its own module, depending on
  the OpenTelemetry API only) wraps each invocation in one server span named by topic, joined to the
  caller's W3C `traceparent`, with `benzene.topic`/`benzene.version`/`benzene.status` attributes, plus
  `benzene.messages.processed`/`benzene.message.duration` metrics. Add it as a middleware in your
  pipeline. Your app owns the OTel SDK/exporter; with no SDK installed the no-op defaults make it
  free. It also provides `TraceContextDecorator` for propagating trace context on outbound calls.
- **Structured logging** — the zero-dependency [`logging`](../logging) package
  (`log/slog` only) emits one structured line per invocation (topic, status, duration; Info/Warn/Error
  by outcome). It's the dependency-free alternative to `diagnostics`.
- **Lambda's built-in trace ID** — the runtime loop sets `_X_AMZN_TRACE_ID` from the invocation's
  `Lambda-Runtime-Trace-Id` header automatically, so AWS X-Ray correlation works without extra wiring.

## Troubleshooting

**404 / handler never called over HTTP** — check the `httpbinding.Route` matches the request method
and path exactly (including `{param}` segments), and that the topic on the route matches a
registered handler's topic. `newHandler`'s HTTP-vs-envelope probe keys on a `requestContext` field;
a caller that sends neither a valid HTTP event nor a valid envelope falls through to
`EnvelopeHandler` and comes back as an error (this is what `TestNewHandler_MalformedEventIsError`
covers).

**SQS/SNS message never routes to a handler** — the topic comes from a `topic` message attribute (or
an envelope in the body), not the raw payload. Confirm the producer sets that attribute, and that a
handler is registered for the matching topic. A record with no resolvable topic gets an empty topic,
which the router maps to a validation error — reported as a batch failure (SQS) or a returned error
(SNS), never silently dropped.

**DynamoDB stream stalls on one record** — the stream is ordered and stops at the first failure,
reporting that record's sequence number so Lambda redelivers from there. If a poison record keeps
failing, the shard won't advance past it until it succeeds or the records age out — that's the
at-least-once, ordered contract, not a bug. Register the `MODIFY`/`REMOVE` topics too if you're only
handling `INSERT` and seeing unhandled records reported as failures.

**`AWS_LAMBDA_RUNTIME_API is not set`** — `awslambda.Start` is meant to run only inside the Lambda
execution environment (which sets that variable). Locally, drive the handler through `benzenetest`
instead of calling `Start`.

**Binary not found / runtime can't start** — the binary must be named `bootstrap` and built for the
architecture declared in the template (`GOARCH` must match `Architectures`). `CGO_ENABLED=0` avoids a
dynamic-linking mismatch against the base image.

**Cold starts** — `App.Run()` executes `GetConfiguration`/`ConfigureServices`/`Configure` once per
execution environment, so cold-start cost is dominated by whatever `ConfigureServices` does. Prefer
lazy initialization inside your services over eager work (e.g. opening a DB connection) there.

## See Also

- [Getting Started](./getting-started.md) — Benzene's core concepts (topics, handlers, `Result`, the
  `App` lifecycle), the prerequisite for this page
- [Azure Functions](./getting-started-azure.md) and [Google Cloud](./getting-started-google.md) —
  the same `App`, other hosts
- [`examples/aws-lambda-helloworld`](../examples/aws-lambda-helloworld) — the complete runnable
  version of this guide
- [`examples/aws-sqs-helloworld`](../examples/aws-sqs-helloworld),
  [`examples/aws-sns-helloworld`](../examples/aws-sns-helloworld),
  [`examples/aws-dynamodb-helloworld`](../examples/aws-dynamodb-helloworld) — the event-source
  transports end to end
- [Transport bindings](https://benzene.app/docs/specification/transport-bindings) and
  [wire contracts](https://benzene.app/docs/specification/wire-contracts) — the language-neutral
  spec every binding here conforms to
