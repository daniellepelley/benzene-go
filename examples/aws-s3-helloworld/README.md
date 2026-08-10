# aws-s3-helloworld

An S3 event-notification **consumer** Lambda. There is no publisher: the "publish" is uploading an
object to an `uploads` S3 bucket, and the bucket's event notification invokes this Lambda.

```
PUT object into `uploads` bucket --> S3 ObjectCreated notification --> consumer Lambda --> handler
```

The topic is `{bucket}:{eventName}` (e.g. `uploads:ObjectCreated:Put`). The notification carries the
object's **metadata** (bucket, key, size, etag) — not its contents; fetch those with an S3 client
using the bucket + key if a handler needs them.

## Layout

Like `aws-kinesis-helloworld` (and unlike `aws-sqs-helloworld`), this example is **not** its own Go
module — it lives in the root `benzene-go` module, because the `awss3` binding it uses is itself
zero-dependency (S3 delivers the notification as plain JSON, so there's no AWS SDK to pull in). The
whole example is one `main.go`.

## Deploy

Requires [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured.

```
cd examples/aws-s3-helloworld
sam build
sam deploy --guided
```

This creates one S3 bucket and the consumer Lambda, wired to the bucket's `s3:ObjectCreated:Put`
notifications (see `template.yaml`).

## CI/CD

`.github/workflows/deploy-aws-s3-helloworld.yml` runs the same `sam build && sam deploy` on every
push to `main` that touches this example (or a package it depends on). It's gated on
`secrets.AWS_ACCESS_KEY_ID` being set — the job is **skipped** (not failed) until you configure the
same secrets/variables as `aws-lambda-helloworld` (see its README): `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, and optionally `AWS_REGION`.

## Try it

```
echo "hello" > hello.txt
aws s3 cp hello.txt "s3://$UPLOADS_BUCKET_NAME/hello.txt"
```

There's no synchronous response — that's the nature of event notifications. To see the result,
check the consumer function's CloudWatch logs (`object uploaded: s3://.../hello.txt (6 bytes, ...)`).

## What this demonstrates

- **`awss3.Handler`**: adapts the Lambda S3 event notification — resolving the topic as
  `{bucket}:{eventName}`, building the object-metadata body so the handler deserializes an ordinary
  struct, and running each record through the pipeline with its own DI scope.
- **Async-invoke failure model**: an S3-to-Lambda notification is an *asynchronous* invocation (no
  batch-item-failure mechanism, unlike SQS/Kinesis event source mappings), so a failed record
  returns a Go error — triggering AWS's own async-invoke retry — rather than being silently dropped.
  This deliberately differs from the .NET binding's fire-and-forget "swallow the failure"; S3
  delivery is at-least-once, so handlers must be idempotent.

## What was verified in this sandbox

This sandbox has no AWS credentials and no reachable container registry, so nothing here was
actually deployed or `docker build`-ed end to end. What *was* verified locally:

- `go test ./examples/aws-s3-helloworld/...` — the app booted from its composition root (`newApp`),
  driven by a native S3 event through `benzenetest.SendS3Event`, asserting on the Go error (valid
  object succeeds; an unregistered bucket surfaces an error for async retry), with no network
  involved.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bootstrap ./examples/aws-s3-helloworld` — the
  exact command the Dockerfile's build stage runs — compiles cleanly to a static ARM64 Linux binary.
- `awss3`'s own test suite (`awss3/s3_test.go`) exercises the binding — topic/body/header
  resolution, the async-error failure path, malformed events — in isolation; this example just wires
  it up.

The deploy workflow's YAML was syntax-checked but has never actually run — it will start running for
real the first time you push to `main` after adding the secrets above.
