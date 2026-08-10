# Benzene for Go

A Go port of [Benzene](https://github.com/daniellepelley/Benzene), the middleware-based framework for
hexagonal (ports-and-adapters) architecture: **write your message handlers once, host them anywhere.**
Zero-dependency at the core and spec-first — it implements the language-neutral Benzene specification
idiomatically in Go and interoperates on the wire with the .NET, TypeScript, and Python ports.

### Documentation

- **Getting started**
  - [Getting started](getting-started.md) — from an empty module to a running HTTP service in a few minutes
    - [AWS Lambda](getting-started-aws.md) — one function over API Gateway, SQS, SNS, DynamoDB, and EventBridge
    - [Azure Functions](getting-started-azure.md) — HTTP, Queue Storage / Service Bus, and Cosmos DB change feed
    - [Google Cloud](getting-started-google.md) — Cloud Run (HTTP) and Pub/Sub
    - [gRPC](getting-started-grpc.md) — the unary gRPC server and client binding
    - [Kafka](getting-started-kafka.md) — the consumer-group worker and producer client

- **Concepts**
  - [Message handlers](message-handlers.md) — handlers, topics, the registry, routing, and dependency injection
  - [Message results](message-result.md) — the `Result[T]` type and the status vocabulary, with the HTTP and gRPC mappings
  - [Middleware](middleware.md) — the pipeline, writing your own middleware, and the built-ins Benzene ships
  - [Testing](testing.md) — unit-test a handler and drive the whole pipeline in-memory with `benzenetest`

- **About**
  - [How Benzene compares](comparison.md) — where the Go port sits relative to Dapr, the Go CDK, Watermill, and Encore
  - [Examples](https://github.com/daniellepelley/benzene-go/tree/main/examples) — runnable services, one per cloud host

- **Specification** — the language-neutral core Benzene is defined by, independent of any one port. The
  cross-language source of truth lives in the [`benzene`](https://github.com/daniellepelley/Benzene/tree/main/docs/specification)
  repo, not here:
  - [Read the specification](https://benzene.app/docs/specification/index.html) — core concepts, wire
    contracts, transport bindings, mesh contracts, the Cloud Service Profile, versioning, the porting
    guide, and the conformance fixtures
