# Cloud-integration parity: benzene-go vs benzene-dotnet

This document assesses how close the Go port is to the .NET reference **for cloud-provider and
messaging-transport integrations** (AWS, Azure, GCP, Kafka, RabbitMQ) — inbound triggers/consumers
*and* outbound publish clients. It is not a claim of exact feature parity: some .NET packages are
ecosystem idioms (ASP.NET hosting, source generators, Autofac, alternate loggers/serializers) with
no meaningful Go analogue. The goal is **near-full parity on the actual cloud integrations**.

Status vocabulary: **Done** (shipped, tested) · **Partial** (covered for the common case, a
sub-feature missing) · **Missing** (no Go equivalent) · **Deferred** (missing on purpose, needs an
SDK / unverifiable shape) · **Out-of-scope** (a .NET-ecosystem idiom, not a cloud integration).

"Verified" for a Go inbound binding means cross-compile + unit tests against the platform's
documented contract — this repo has no live cloud credentials, and per its no-fabrication rule an
unverifiable wire shape is deferred, not guessed.

## Headline

- **AWS inbound: full parity.** All seven Lambda triggers are done (API Gateway v1/v2/Function-URL/ALB,
  SQS, SNS, EventBridge, DynamoDB Streams, Kinesis, Kafka/MSK, S3).
- **AWS outbound: SQS, SNS, EventBridge done.** Missing: Lambda-invoke and Step Functions clients, and
  a self-hosted SQS poller (all `aws-sdk-go-v2`, isolatable in a module like `awssqs`).
- **Azure Functions triggers: parity on the custom-handler-expressible ones** (Queue Storage, Service
  Bus*, Event Grid, Timer, Cosmos change feed). Kafka trigger missing; Event Hub / Blob are SDK-typed
  and deferred. *Service Bus lacks explicit per-message settle/dead-letter.
- **Azure outbound + self-hosted workers: all missing** — every one needs an Azure SDK.
- **GCP: Pub/Sub push (Cloud Run) done.** Pub/Sub outbound and the Functions-framework Gen2 flavors
  need GCP SDKs.
- **Kafka self-hosted: done.** RabbitMQ: missing (needs an AMQP client).

## AWS

| .NET package | What it is | Go status | Dependency to close |
|---|---|---|---|
| `Aws.Lambda.Core` | Runtime bootstrap + event router | Done `awslambda` | — |
| `Aws.Lambda.ApiGateway` | Inbound HTTP (v1/v2/authorizer) | Done `awslambda` (custom-authorizer sub-feature not ported) | — |
| `Aws.Lambda.Sqs` | Inbound SQS trigger | Done `awssqs.Handler` | — |
| `Aws.Lambda.Sns` | Inbound SNS trigger | Done `awssns.Handler` | — |
| `Aws.Lambda.EventBridge` | Inbound EventBridge trigger | Done `awseventbridge.Handler` | — |
| `Aws.Lambda.DynamoDb` | Inbound DynamoDB Streams | Done `awsdynamodb` | — |
| `Aws.Lambda.Kinesis` | Inbound Kinesis | Done `awskinesis` | — |
| `Aws.Lambda.Kafka` | Inbound MSK/Kafka | Done `awskafka` | — |
| `Aws.Lambda.S3` | Inbound S3 notifications | Done `awss3` | — |
| `Clients.Aws.Sqs` | Outbound SQS publish | Done `awssqs.Client` | — |
| `Clients.Aws.Sns` | Outbound SNS publish | Done `awssns.Client` | — |
| `Clients.Aws.EventBridge` | Outbound `PutEvents` | Done `awseventbridge.Client` | — |
| `Clients.Aws.Lambda` | **Outbound Lambda-invoke** (RequestResponse/Event, FunctionError) | **Done** `awslambdaclient` | `aws-sdk-go-v2/service/lambda` |
| `Clients.Aws.StepFunctions` | **Outbound StartExecution** (idempotent name) | **Done** `awsstepfunctions` | `aws-sdk-go-v2/service/sfn` |
| `Aws.Sqs` (not the Lambda one) | **Self-hosted SQS poller** (Receive/Delete loop, backoff) | **Done** `awssqs.Consumer` | `aws-sdk-go-v2/service/sqs` (already present) |
| `Aws.Lambda.XRay` | Direct-to-X-Ray-SDK per-stage subsegments | Partial — intent covered by `diagnostics` (OTel→X-Ray via ADOT/OTLP); a direct-SDK port is a separate gap | `aws-xray-sdk-go` (only if the OTel path is judged inadequate) |
| `Aws.Lambda.HttpBridge` / `.AspNet` / `.Hosting` | Bridge to ASP.NET / generic-host glue | Out-of-scope (no ASP.NET in Go; `net/http` handler *is* the pipeline) | — |

## Azure

Every `Azure.Function.*` package is .NET **isolated-worker** (Azure SDK trigger types). The Go port
uses the **custom-handler** model (the Functions host POSTs a `Data`/`Metadata` JSON envelope to a
plain HTTP server). So parity turns on whether a trigger reduces to a plain string/JSON in that
envelope (Queue/ServiceBus/Cosmos/Timer/EventGrid/Kafka do) or a rich SDK object / lease-owning
stream (Event Hub, Blob do not).

| .NET package | What it is | Go status | Dependency to close |
|---|---|---|---|
| `Azure.Function.QueueStorage` | Inbound Storage-Queue trigger (string body) | Done `azurefunctions.QueueHandler` | — |
| `Azure.Function.ServiceBus` | Inbound Service-Bus trigger (string body) + **explicit settle** (Complete/Abandon/dead-letter) | **Partial** — `QueueHandler` covers the string body with outer-status redelivery (≈ AutoComplete); no explicit settle / dead-letter / sessions | — for the covered subset; explicit settle likely needs `azservicebus` |
| `Azure.Function.EventGrid` | Inbound Event-Grid trigger | Done `azurefunctions.EventGridHandler` | — |
| `Azure.Function.Timer` | Inbound Timer trigger | Done `azurefunctions.TimerHandler` | — |
| `Azure.Function.CosmosDb` | Inbound Cosmos change-feed (host owns feed) | Done `azurefunctions.CosmosHandler` | — |
| `Azure.Function.Kafka` | Inbound Kafka trigger (value bytes→string) | **Missing** — zero-dep-achievable via the custom-handler envelope, but the exact payload shape must be pinned against a live Functions host first (no fabrication) | — (inbound), pending shape verification |
| `Azure.Function.EventHub` | Inbound Event-Hub trigger — SDK-typed `EventData`, batch, checkpoint | **Deferred** (not a clean custom-handler JSON contract) | `azure-sdk-for-go/.../azeventhubs` |
| `Azure.Function.BlobStorage` | Inbound Blob trigger — SDK-typed `byte[]` + lease | **Deferred** | `azure-sdk-for-go/.../azblob` |
| `Azure.ServiceBus` | **Self-hosted** Service-Bus worker (settle/dead-letter) | **Missing** | `azure-sdk-for-go/.../azservicebus` |
| `Azure.EventHub` | **Self-hosted** Event-Hub worker (+ checkpoint store) | **Missing/Deferred** | `azure-sdk-for-go/.../azeventhubs` |
| `Azure.CosmosDb` | **Self-hosted** Cosmos change-feed worker (owns lease) | **Deferred** | `azure-sdk-for-go/.../azcosmos` |
| `Clients.Azure.ServiceBus` | Outbound Service-Bus publish | **Missing** | `azure-sdk-for-go/.../azservicebus` |
| `Clients.Azure.EventHub` | Outbound Event-Hub publish | **Missing** | `azure-sdk-for-go/.../azeventhubs` |
| `Clients.Azure.EventGrid` | Outbound Event-Grid publish | **Missing** | `azure-sdk-for-go/.../azeventgrid` |
| `Clients.Azure.QueueStorage` | Outbound Storage-Queue send | **Missing** | `azure-sdk-for-go/.../azqueue` |
| `Azure.Function.SourceGenerators` / `.AspNet` | Codegen / ASP.NET hosting | Out-of-scope | — |

## GCP

| .NET package | What it is | Go status | Dependency to close |
|---|---|---|---|
| `GoogleCloud.Functions.Http` | Cloud Functions Gen2 HTTP (functions-framework) | Partial — Go targets **Cloud Run** instead (`httpbinding`, `examples/gcp-cloudrun-helloworld`); the Gen2 buildpack path is deferred | `GoogleCloudPlatform/functions-framework-go` |
| `GoogleCloud.Functions.PubSub` | Inbound Pub/Sub CloudEvent trigger (functions-framework) | Partial — Go `gcppubsub` covers the **push-subscription HTTP** path (zero-dep, the common Cloud Run shape); the functions-framework CloudEvent flavor is not ported | `functions-framework-go` (for that flavor) |
| `Clients.GoogleCloud.PubSub` | Outbound Pub/Sub publish | **Missing** (inbound push half is done) | `cloud.google.com/go/pubsub` |

## Kafka + RabbitMQ

| .NET package | What it is | Go status | Dependency to close |
|---|---|---|---|
| `Kafka.Core` | Self-hosted consumer-group loop + producer | Done `kafka` module | — |
| `RabbitMq` | **Self-hosted** worker + outbound publish | **Missing** (both halves) | `github.com/rabbitmq/amqp091-go` |

## Prioritized plan to close the gap

### A. Zero-dependency, buildable now
1. **Azure Functions Kafka trigger** (`azurefunctions.KafkaHandler`) — custom-handler string/bytes body
   on the existing `Data`/`Metadata` envelope. **Blocked on** verifying the exact custom-handler Kafka
   payload against Azure's documented shape before shipping (repo no-fabrication rule).

That is essentially the *only* remaining zero-dep cloud gap — everything else below needs an SDK.

### B. Needs a specific third-party dependency (each isolated in its own module, like `awssqs`)
- **AWS** (`aws-sdk-go-v2/service/*` — same family already used by `awssqs`/`awssns`/`awseventbridge`):
  Lambda-invoke client (`lambda`), Step Functions client (`sfn`), self-hosted SQS poller (`sqs`, dep
  already present in the `awssqs` module).
- **Azure** (`github.com/Azure/azure-sdk-for-go/sdk/...`): outbound Service Bus (`azservicebus`),
  Event Hub (`azeventhubs`), Event Grid (`azeventgrid`), Queue Storage (`azqueue`); self-hosted
  Service Bus / Event Hub workers; Cosmos change-feed worker (`azcosmos`); the isolated-worker Event
  Hub / Blob triggers.
- **GCP**: outbound Pub/Sub (`cloud.google.com/go/pubsub`); Functions-framework Gen2 target
  (`functions-framework-go`).
- **RabbitMQ**: self-hosted worker + outbound client (`github.com/rabbitmq/amqp091-go`).

### C. Out-of-scope (.NET-ecosystem idioms, not cloud integrations)
ASP.NET-on-Lambda/Functions bridges and generic-host glue (`Aws.Lambda.HttpBridge`/`.AspNet`/
`.Hosting`, `Azure.Function.AspNet`); source generators / `CodeGen.*`; Autofac, alternate
loggers/serializers, `Datadog`/`Zipkin` vendor observability. Go fills these roles with `net/http`,
the first-party `Container`/`Scope`, `log/slog`, `encoding/json`, and OTLP export.

## The dependency decision

Closing the cloud-integration gap is now **overwhelmingly an SDK question**, because the remaining
work is outbound clients and self-hosted broker workers, none of which can be done with the standard
library. The repo keeps the **root module zero-dependency** and isolates each approved third-party
dependency in its own module (`awssqs`, `awssns`, `awseventbridge`, `kafka`, `diagnostics`,
`grpcbinding`). Extending cloud parity means approving new modules on that same pattern — the choice
of *which* SDKs to take on is a maintenance-surface decision recorded here for an explicit yes.
