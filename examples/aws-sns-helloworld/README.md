# aws-sns-helloworld

A full round trip through SNS: a **publisher** Lambda (fronted by a Function URL) forwards each
request onto an SNS topic instead of processing it locally; a **consumer** Lambda, subscribed to
that topic, actually runs the greet handler.

```
HTTP POST /greet --> publisher Lambda --> SNS topic --> consumer Lambda --> greet handler
```

## Layout

```
greeting/     shared GreetRequest/GreetResponse/Handler - used by consumer, referenced by publisher
publisher/    HTTP-triggered Lambda; forwards to SNS via awssns.Client, never runs Handler itself
consumer/     SNS-triggered Lambda; runs Handler via awssns.Handler
```

This is its own Go module (see `go.mod`) - it depends on both the root `benzene-go` module
(core + `awslambda` + `httpbinding`) and the `awssns` module, so putting it inside either of
those would create a dependency cycle.

## Deploy

Requires [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured.

```
cd examples/aws-sns-helloworld
sam build
sam deploy --guided
```

This creates one SNS topic and both Lambda functions - the consumer is subscribed directly to
the topic (see `template.yaml`).

## CI/CD

`.github/workflows/deploy-aws-sns-helloworld.yml` runs the same `sam build && sam deploy` on
every push to `main` that touches this example or a package it depends on. It's gated on
`secrets.AWS_ACCESS_KEY_ID` being set - the job is **skipped** (not failed) until you configure
the same secrets/variables as `aws-lambda-helloworld` (see its README): `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, and optionally `AWS_REGION`.

## Try it

```
curl -X POST "$PUBLISHER_FUNCTION_URL/greet" -d '{"name":"World"}'
# 202 Accepted - the request has been forwarded to the topic, not yet processed
```

There's no synchronous response with the actual greeting - that's the nature of async
messaging. To see the result, check the consumer function's CloudWatch logs.

Unlike the `aws-sqs-helloworld` example, a message that fails validation here (an empty `name`,
which `greeting.Handler` rejects) isn't reported as a structured partial-batch failure - SNS's
direct Lambda subscription has no such mechanism. Instead, the consumer returns a plain Go error,
which triggers AWS's own async-invoke retry (and, if configured, a dead-letter queue) - see
`awssns.Handler`'s doc comment and `consumer/main_test.go`'s
`TestConsumer_MissingNameIsReturnedAsError`.

## What this demonstrates

- **`awssns.Handler`** (consumer): adapts a direct SNS-to-Lambda subscription's invocation
  payload, resolving each notification's topic from its `topic` message attribute, running it
  through the pipeline with its own DI scope, and returning a Go error - triggering AWS's own
  retry - if any notification's dispatch was not a success.
- **`awssns.Client`** (publisher): publishes via `Publish`, writing the topic as a message
  attribute - the send-side counterpart of the same wire-contracts.md §2 convention the
  consumer reads.
- Both satisfy the shape this repo's `awslambda`/`client` packages already establish: every
  outcome is a `Result`, real AWS failures map to `ServiceUnavailable`.

## What was verified in this sandbox

This sandbox has no AWS credentials and no reachable container registry, so nothing here was
actually deployed. What *was* verified locally:

- `go test ./examples/aws-sns-helloworld/...` - both Lambda functions' wiring, using a fake SNS
  API (no real AWS calls) for the publisher and a hand-built SNS event JSON for the consumer.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build` for both `consumer` and `publisher` - the
  exact command each Dockerfile's build stage runs - compiles cleanly.
- `awssns`'s own test suite (`awssns/sns_test.go`, `awssns/client_test.go`) exercises the
  underlying contract in isolation - this example just wires it up.

The deploy workflow's YAML was syntax-checked but has never actually run - it will start
running for real the first time you push to `main` after adding the secrets above.
