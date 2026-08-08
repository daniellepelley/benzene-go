# aws-kinesis-helloworld

A Kinesis Data Streams **consumer** Lambda. There is no publisher: the "publish" is a `PutRecord`
onto a Kinesis `orders` stream, and the stream's event source mapping turns each record into an
invocation this Lambda consumes.

```
PutRecord onto `orders` stream --> Kinesis event source mapping --> consumer Lambda --> orders handler
```

The stream name is the topic (a Kinesis record has no per-record event type, so the stream itself
is the routing key — unlike DynamoDB Streams' `{table}:{event}`). The record data is base64-decoded
back to the JSON the producer wrote before the handler sees it.

## Layout

Like `aws-dynamodb-helloworld` (and unlike `aws-sqs-helloworld`), this example is **not** its own
Go module — it lives in the root `benzene-go` module, because the `awskinesis` binding it uses is
itself zero-dependency (AWS delivers the records as plain JSON, so there's no AWS SDK to pull in and
no cycle to avoid). The whole example is one `main.go`.

## Deploy

Requires [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured.

```
cd examples/aws-kinesis-helloworld
sam build
sam deploy --guided
```

This creates one Kinesis stream (on-demand) and the consumer Lambda, wired to the stream with
`FunctionResponseTypes: [ReportBatchItemFailures]` (see `template.yaml`) — so a record that fails is
resumed from its sequence number rather than silently checkpointed past.

## CI/CD

`.github/workflows/deploy-aws-kinesis-helloworld.yml` runs the same `sam build && sam deploy` on
every push to `main` that touches this example (or a package it depends on). It's gated on
`secrets.AWS_ACCESS_KEY_ID` being set — the job is **skipped** (not failed) until you configure the
same secrets/variables as `aws-lambda-helloworld` (see its README): `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, and optionally `AWS_REGION`.

## Try it

```
aws kinesis put-record \
  --stream-name "$ORDERS_STREAM_NAME" \
  --partition-key o-1 \
  --data "$(printf '{"id":"o-1","item":"widget","amount":9.99}' | base64)"
```

There's no synchronous response — that's the nature of stream processing. To see the result, check
the consumer function's CloudWatch logs (`order received: id=o-1 item="widget" amount=9.99`). A
record with no `id` (which `orderReceived` rejects) shows up there as a failed batch item — reported
by its stream sequence number for redelivery, not as an error back to the producer.

## What this demonstrates

- **`awskinesis.Handler`**: adapts the Lambda Kinesis event source mapping's batch payload —
  resolving the topic as the stream name (parsed from the record's stream ARN), base64-decoding the
  record data into the producer's JSON so the handler deserializes an ordinary struct, and running
  each record through the pipeline with its own DI scope.
- **Ordered, stop-at-first-failure batching**: records are ordered within a shard, so they are
  processed sequentially and processing stops at the first unsuccessful one, reporting that record's
  `SequenceNumber` as the sole `batchItemFailures` entry. AWS resumes the batch from there —
  deliberately not `awssqs`'s concurrent fan-out (it is the same shape as `aws-dynamodb-helloworld`).

## What was verified in this sandbox

This sandbox has no AWS credentials and no reachable container registry, so nothing here was
actually deployed or `docker build`-ed end to end. What *was* verified locally:

- `go test ./examples/aws-kinesis-helloworld/...` — the app booted from its composition root
  (`newApp`), driven by a native Kinesis record through `benzenetest.SendKinesisStream`, asserting on
  the `batchItemFailures` response (valid record, missing-id failure, unrouted stream), with no
  network involved.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bootstrap ./examples/aws-kinesis-helloworld` —
  the exact command the Dockerfile's build stage runs — compiles cleanly to a static ARM64 Linux
  binary.
- `awskinesis`'s own test suite (`awskinesis/kinesis_test.go`) exercises the binding — topic/stream
  resolution, base64 decode, stop-at-first-failure, poison records — in isolation; this example just
  wires it up.

The deploy workflow's YAML was syntax-checked but has never actually run — it will start running for
real the first time you push to `main` after adding the secrets above.
