# aws-sqs-helloworld

A full round trip through SQS: a **publisher** Lambda (fronted by a Function URL) forwards each
request onto an SQS queue instead of processing it locally; a **consumer** Lambda, triggered by
that queue, actually runs the greet handler.

```
HTTP POST /greet --> publisher Lambda --> SQS queue --> consumer Lambda --> greet handler
```

## Layout

```
greeting/     shared GreetRequest/GreetResponse/Handler - used by consumer, referenced by publisher
publisher/    HTTP-triggered Lambda; forwards to SQS via awssqs.Client, never runs Handler itself
consumer/     SQS-triggered Lambda; runs Handler via awssqs.Handler
```

This is its own Go module (see `go.mod`) - it depends on both the root `benzene-go` module
(core + `awslambda` + `httpbinding`) and the `awssqs` module, so putting it inside either of
those would create a dependency cycle.

## Deploy

Requires [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured.

```
cd examples/aws-sqs-helloworld
sam build
sam deploy --guided
```

This creates one SQS queue and both Lambda functions - the consumer's event source mapping is
configured with `FunctionResponseTypes: [ReportBatchItemFailures]` (see `template.yaml`), so a
message that fails validation is retried on its own rather than poisoning the whole batch.

## CI/CD

`.github/workflows/deploy-aws-sqs-helloworld.yml` runs the same `sam build && sam deploy` on
every push to `main` that touches this example or a package it depends on. It's gated on
`secrets.AWS_ACCESS_KEY_ID` being set - the job is **skipped** (not failed) until you configure
the same secrets/variables as `aws-lambda-helloworld` (see its README): `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, and optionally `AWS_REGION`.

## Try it

```
curl -X POST "$PUBLISHER_FUNCTION_URL/greet" -d '{"name":"World"}'
# 202 Accepted - the request has been forwarded to the queue, not yet processed
```

There's no synchronous response with the actual greeting - that's the nature of async
messaging. To see the result, check the consumer function's CloudWatch logs, or note that an
empty `name` (which `greeting.Handler` rejects) shows up there as a failed batch item, not as
an HTTP error back to the original caller - see `publisher/main_test.go`'s
`TestPublisher_DoesNotValidateContentBeforeForwarding` for why: the publisher deliberately
doesn't duplicate the consumer's validation.

## What this demonstrates

- **`awssqs.Handler`** (consumer): adapts the Lambda SQS event source mapping's batch payload,
  resolving each record's topic from its `topic` message attribute, running each through the
  pipeline with its own DI scope, and reporting per-message failures back via
  `batchItemFailures`.
- **`awssqs.Client`** (publisher): publishes via `SendMessage`, writing the topic as a message
  attribute - the send-side counterpart of the same wire-contracts.md §2 convention the
  consumer reads.
- Both satisfy the shape this repo's `awslambda`/`client` packages already establish: every
  outcome is a `Result`, real AWS failures map to `ServiceUnavailable`.

## What was verified in this sandbox

This sandbox has no AWS credentials and no reachable container registry, so nothing here was
actually deployed. What *was* verified locally:

- `go test ./examples/aws-sqs-helloworld/...` - both Lambda functions' wiring, using a fake SQS
  API (no real AWS calls) for the publisher and a hand-built SQS event JSON for the consumer.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build` for both `consumer` and `publisher` - the
  exact command each Dockerfile's build stage runs - compiles cleanly.
- `awssqs`'s own test suite (`awssqs/sqs_test.go`, `awssqs/client_test.go`) exercises the
  underlying contract in isolation - this example just wires it up.

The deploy workflow's YAML was syntax-checked but has never actually run - it will start
running for real the first time you push to `main` after adding the secrets above.
