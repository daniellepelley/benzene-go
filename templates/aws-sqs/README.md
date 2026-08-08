# {{MODULE}} — Benzene on AWS Lambda + SQS

A [Benzene](https://github.com/daniellepelley/Benzene) service on AWS Lambda, triggered by an SQS
queue. Generated from the benzene-go `aws-sqs` template.

## What's here

| File | Purpose |
|---|---|
| `main.go` | Composition root (`newApp`), the demo `greetHandler`, and the Lambda entry point (`awssqs.Handler`) that drains each SQS batch through the pipeline. |
| `greeter.go` | The one injected dependency — a `Greeter` port and its default adapter, wired up in `newApp`. Your handler resolves it from the DI scope; swap the adapter without touching the handler. |
| `main_test.go` | A component test: boots the real app and pushes a native SQS event through the whole pipeline, asserting (via a spy `Greeter`) that the handler actually ran, and that a rejected message comes back as a reported batch-item failure. |
| `template.yaml` | AWS SAM template — an SQS queue and a container-image Lambda triggered by it, with `ReportBatchItemFailures`. |
| `Dockerfile` | Builds the `bootstrap` binary for the `provided.al2023` custom runtime. |

## Build and test

```bash
go build ./...
go test ./...
```

## Deploy

Requires the [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured. `template.yaml` is hand-checked against the pattern
in the Benzene docs but **not** validated or deployed from this template — review it first.

```bash
sam build
sam deploy --guided
```

This creates one SQS queue and the consumer Lambda. To exercise it, send a message to the queue
with a `topic` message attribute of `greet` and a JSON body like `{"name":"World"}` — Benzene
routes by the `topic` attribute (wire-contracts.md §2). There's no synchronous response: that's
the nature of async messaging. The result shows up in the function's CloudWatch logs; a message
the handler rejects (e.g. an empty `name`) is reported as a failed batch item and redelivered.

## Where to go next

- **`greetHandler` in `main.go`** is where your logic goes — replace it, or add more handlers,
  registering each on its own topic in `newApp`.
- **`greeter.go`** shows the injected-dependency pattern: depend on an interface, register a
  concrete adapter, resolve it in the handler via `benzene.ScopeFromContext` +
  `benzene.GetService`.
- A handler routes by **topic**, so the same handler runs unchanged on every Benzene transport.
  Want an HTTP front door instead? Start from the `aws-apigateway` template — same handler shape,
  a different trigger.
- Publishing to the queue from another service uses `awssqs.Client` (the send-side counterpart) —
  see the [benzene-go](https://github.com/daniellepelley/benzene-go) `awssqs` package and the
  `aws-sqs-helloworld` example for the full publisher/consumer round trip.
