# aws-lambda-helloworld

The `helloworld` service, deployed to AWS Lambda as a container image with a Function URL -
no API Gateway resource needed. One handler answers both an HTTP request (via the Function
URL) and a direct/Lambda-to-Lambda envelope invoke.

## Deploy

Requires [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured.

```
cd examples/aws-lambda-helloworld
sam build
sam deploy --guided
```

`sam deploy --guided` walks you through a stack name, region, and confirms creating the
Function URL with public (`AuthType: NONE`) access - see `template.yaml`. The output includes
the Function URL to curl.

## CI/CD

`.github/workflows/deploy-aws-lambda-helloworld.yml` runs the same `sam build && sam deploy` on
every push to `main` that touches this example (or a package it depends on). It's gated on
`secrets.AWS_ACCESS_KEY_ID` being set - the job is **skipped** (not failed) until you configure:

| Name | Kind | Value |
|---|---|---|
| `AWS_ACCESS_KEY_ID` | secret | An IAM access key with permission to deploy the stack (Lambda, ECR, IAM role creation, CloudFormation) |
| `AWS_SECRET_ACCESS_KEY` | secret | The matching secret key |
| `AWS_REGION` | variable (optional) | Deploy region; defaults to `us-east-1` |

## Try it

```
curl -X POST "$FUNCTION_URL/greet" -d '{"name":"World"}'
# {"greeting":"Hello, World!"}

curl -X POST "$FUNCTION_URL/greet" -d '{"name":""}'
# 400 Bad Request
```

## How this differs from the plain HTTP example

`examples/helloworld` listens on a TCP port with `net/http`. Lambda doesn't work that way - a
custom-runtime Lambda process polls the [Lambda Runtime
API](https://docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html) for the next invocation
event and posts back a response, over and over, for the life of the execution environment. The
`awslambda` package (`Start`, `HTTPHandler`, `EnvelopeHandler`) implements that loop; `main.go`
here just wires the same `greetHandler` from the plain HTTP example into it instead.

## What was verified in this sandbox

This sandbox has no AWS credentials and no reachable container registry, so nothing here was
actually deployed or `docker build`-ed end to end. What *was* verified locally:

- `go test ./examples/aws-lambda-helloworld/...` - `newHandler`'s dispatch (Function-URL event
  vs. envelope event) against both event shapes, including the failure and malformed-event
  paths, with no network involved (see `main_test.go`).
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bootstrap ./examples/aws-lambda-helloworld`
  - the exact command the Dockerfile's build stage runs - compiles cleanly to a static ARM64
  Linux binary.
- `awslambda`'s own test suite exercises the Runtime API bootstrap loop itself against a fake
  local server standing in for the real one (see `awslambda/runtime_test.go`).

Deploying for real additionally needs: the container image to actually push/pull (this
sandbox's network policy blocks the public ECR/Docker Hub registries this Dockerfile pulls
from), and a real AWS account for `sam deploy` to target. The deploy workflow's YAML was
syntax-checked but has never actually run - it will start running for real the first time you
push to `main` after adding the secrets above.
