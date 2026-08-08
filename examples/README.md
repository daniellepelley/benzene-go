# benzene-go examples

Runnable sample services that each **show off a technique** — hosting the greet handler on a given
transport/cloud, doing gRPC or Kafka, tracing with OpenTelemetry, standing up a mesh. Read one to learn
the technique, then borrow it into your own service.

> **These are demos, not starting points.** An example is contrived to show one thing, so it carries
> boilerplate you'd delete when adopting it. To *start* a new service, generate a vanilla starter from
> [`../templates/`](../templates) with `gonew` and write your handlers into it. Templates are where you
> start; examples are where you learn a technique.

Each example is its own buildable Go module (wired into the repo's `go.work`) with a `main.go`, a
`main_test.go` that drives a real message through the pipeline with `benzenetest`, and a `README.md`.

## The examples

| Example | Shows |
|---|---|
| [`helloworld`](helloworld) | The minimal shape — a handler behind a port, wired through a composition root |
| [`http-helloworld`](http-helloworld) | Hosting on a standalone `net/http` server via `httpbinding` |
| [`grpc-helloworld`](grpc-helloworld) | A gRPC unary RPC via `grpcbinding`, with an outbound client |
| [`kafka-helloworld`](kafka-helloworld) | A Kafka consumer group via the `kafka` package, with an outbound producer |
| [`opentelemetry-helloworld`](opentelemetry-helloworld) | Pipeline tracing via the `diagnostics` middleware (real OTel exporter) |
| [`aws-lambda-helloworld`](aws-lambda-helloworld) | AWS Lambda as a container image behind a Function URL |
| [`aws-sqs-helloworld`](aws-sqs-helloworld) | A full SQS round trip — publisher Lambda → queue → consumer Lambda |
| [`aws-sns-helloworld`](aws-sns-helloworld) | The same round trip over SNS |
| [`aws-dynamodb-helloworld`](aws-dynamodb-helloworld) | A DynamoDB Streams consumer Lambda |
| [`azure-functions-helloworld`](azure-functions-helloworld) | Azure Functions custom handler |
| [`gcp-cloudrun-helloworld`](gcp-cloudrun-helloworld) | Google Cloud Run |
| [`gcp-pubsub-helloworld`](gcp-pubsub-helloworld) | A Google Cloud Pub/Sub push subscriber |
| [`mesh-helloworld`](mesh-helloworld) | The whole Benzene Mesh story end to end |
