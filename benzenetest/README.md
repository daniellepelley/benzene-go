# benzenetest

An in-process test host for applications built on `benzene-go` - the Go counterpart to the main
[daniellepelley/Benzene](https://github.com/daniellepelley/Benzene) repo's `Benzene.Testing` /
`BenzeneTestHost`. Use it in *your own* application's tests to boot your real service from its
composition root, push a message in the transport's native shape through the front door, and
assert on what comes back **and** on what the service published - swapping any dependency for a
fake. The only thing that changes between an AWS Lambda test and a GCP Pub/Sub test is a single
`Send*` call.

## End-to-end: front door in, native response out, assert on egress

```go
fake := benzenetest.NewFakeMessageSender()

host := benzenetest.NewHost(newApp(realClient),                    // 1. boot the REAL app from its composition root
    benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {  // 2. override ANY registration with a fake
        client.RegisterSender(b.Container, fake)
    }),
    benzenetest.WithRoutes(routes()...),                            // HTTP route table, for HTTP-shaped hosts
)

resp := benzenetest.SendAPIGateway(t, host, "POST", "/orders", order, nil) // 3+4. native event in, native response out

require.Equal(t, 202, resp.StatusCode)                             // 5a. assert on the transport response
require.Equal(t, benzene.NewTopic("order:created"), fake.LastTopic()) // 5b. assert on the egress
```

To run the **same handlers** on GCP, only line 3 changes to
`benzenetest.SendPubSub(t, host, benzene.NewTopic("order:create"), order, nil)`. Lines 1, 2, and 5
are identical - that parallelism is the point.

### The parallel `Send*` set

| Transport | Specialization + dispatch | Native response |
|-----------|---------------------------|-----------------|
| Native HTTP (`net/http`) | `benzenetest.SendHTTP(t, host, method, path, payload, headers)` | `HTTPResponse` |
| AWS API Gateway / Function URL | `benzenetest.SendAPIGateway(t, host, method, path, payload, headers)` | `HTTPResponse` |
| AWS Lambda envelope (direct invoke) | `benzenetest.SendEnvelope(t, host, topic, payload, headers)` | `wire.Response` |
| AWS SQS | `awssqs.SendSQS(t, host, topic, payload, headers)` | `awssqs.SQSResponse` |
| AWS SNS | `awssns.SendSNS(t, host, topic, payload, headers)` | `error` |
| AWS DynamoDB Streams | `benzenetest.SendDynamoDBStream(t, host, eventName, tableName, sequenceNumber, newImage)` | `[]string` (failed sequence numbers) |
| AWS Kinesis Data Streams | `benzenetest.SendKinesisStream(t, host, streamName, sequenceNumber, payload)` | `[]string` (failed sequence numbers) |
| AWS Lambda MSK / Kafka | `benzenetest.SendKafkaEvent(t, host, topic, partition, offset, payload)` | `[]string` (failed records as `"{partition}@{offset}"`) |
| AWS S3 event notification | `benzenetest.SendS3Event(t, host, bucket, eventName, key)` | `error` (nil ok; non-nil triggers async retry) |
| GCP Pub/Sub | `benzenetest.SendPubSub(t, host, topic, payload, headers)` | `HTTPResponse` (204 ack / 500 nack) |
| Azure Functions HTTP | `benzenetest.SendAzureHTTP(t, host, method, path, payload, headers)` | `HTTPResponse` |
| Azure Functions queue | `benzenetest.SendAzureQueue(t, host, dataName, path, topic, payload, headers)` | `HTTPResponse` |
| Azure Cosmos DB Change Feed | `benzenetest.SendCosmosChangeFeed(t, host, dataName, path, topic, documents)` | `HTTPResponse` (200 checkpoint / 500 redeliver) |
| Azure Functions Timer | `benzenetest.SendTimer(t, host, dataName, path, topic, tick)` | `HTTPResponse` (200 ok / 500 failed) |

`SendSQS`/`SendSNS` live in the `awssqs`/`awssns` modules (which carry the AWS SDK) rather than in
`benzenetest`, so the neutral package stays free of cloud SDK dependencies; the naming, argument
order, and return shapes stay parallel with the rest. Each `Send*` builds the transport's native
event from `(topic/route, payload, headers)` via a `benzenetest.New*Event` builder, dispatches it
through that transport's real binding, and hands back the framework-mapped native response.

`WithServices` runs after your app's own `ConfigureServices` but before `Configure` builds the
pipeline - last-registration-wins - so it reaches any container registration. `FakeMessageSender`
implements `client.Sender` and records `LastTopic()` / `LastMessage()` / `LastHeaders()`, so a
test proves ingress -> handler -> egress carries the payload through, not only the topic.

## Message-level: `Invoke`

When you only need to drive one handler through the pipeline without a transport at all (a focused
unit test of middleware or DI wiring), `Invoke` runs one in-process invocation against a built
`*benzene.ApplicationBuilder`:

```go
result := benzenetest.Invoke[GreetRequest, GreetResponse](
    context.Background(),
    builder, // your *benzene.ApplicationBuilder, exactly as App.Run() returns it
    benzene.NewTopic("greet"),
    nil, // headers
    GreetRequest{Name: "World"},
)
```

`request` is passed straight through as the raw request value - no JSON round-trip - so middleware,
DI resolution (`benzene.ScopeFromContext`), and the router's own dispatch all run for real. A
pipeline error or a missing result becomes `ServiceUnavailable`/`UnexpectedError`, matching every
other binding's "every outcome is a `Result`, never a raw error" rule. Prefer the end-to-end
`Send*` helpers for feature/integration tests; keep `Invoke` for genuinely message-level units.
