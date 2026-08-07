# aws-dynamodb-helloworld

A DynamoDB Streams **consumer** Lambda. There is no publisher: the "publish" is an ordinary
write to a DynamoDB `orders` table, and the table's stream turns that write into a change
record this Lambda consumes.

```
PutItem into `orders` table --> DynamoDB stream --> consumer Lambda --> orders:INSERT handler
```

The change type becomes the topic suffix, so one table feeds three topics -
`orders:INSERT`, `orders:MODIFY`, `orders:REMOVE`. This example registers `orders:INSERT`
only; a `MODIFY`/`REMOVE` record routes to no handler and is left for redelivery (register
those topics too to consume them).

## Layout

Unlike `aws-sqs-helloworld`, this example is **not** its own Go module - it lives in the root
`benzene-go` module, because the `awsdynamodb` binding it uses is itself zero-dependency
(AWS delivers the change records as plain JSON to the invocation, so there's no AWS SDK to pull
in and therefore no cycle to avoid). The whole example is one `main.go`.

## Deploy

Requires [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured.

```
cd examples/aws-dynamodb-helloworld
sam build
sam deploy --guided
```

This creates one DynamoDB table (`StreamViewType: NEW_AND_OLD_IMAGES`) and the consumer
Lambda, wired to the table's stream with `FunctionResponseTypes: [ReportBatchItemFailures]`
(see `template.yaml`) - so a record that fails is retried from its sequence number rather than
silently checkpointed past.

## CI/CD

`.github/workflows/deploy-aws-dynamodb-helloworld.yml` runs the same `sam build && sam deploy`
on every push to `main` that touches this example (or a package it depends on). It's gated on
`secrets.AWS_ACCESS_KEY_ID` being set - the job is **skipped** (not failed) until you configure
the same secrets/variables as `aws-lambda-helloworld` (see its README): `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, and optionally `AWS_REGION`.

## Try it

```
aws dynamodb put-item \
  --table-name "$ORDERS_TABLE_NAME" \
  --item '{"id":{"S":"o-1"},"item":{"S":"widget"},"amount":{"N":"9.99"}}'
```

There's no synchronous response - that's the nature of stream processing. To see the result,
check the consumer function's CloudWatch logs (`order placed: id=o-1 item="widget"
amount=9.99`). A row with no `id` (which `orderInserted` rejects) shows up there as a failed
batch item - reported by its stream sequence number for redelivery, not as an error back to
whoever wrote the row.

## What this demonstrates

- **`awsdynamodb.Handler`**: adapts the Lambda DynamoDB stream event source mapping's batch
  payload - resolving each record's topic as `{tableName}:{eventName}` (the table parsed from
  the stream ARN), unmarshalling the record's image from DynamoDB AttributeValue format into
  plain JSON so the handler deserializes an ordinary struct, and running each record through
  the pipeline with its own DI scope.
- **Ordered, stop-at-first-failure batching**: a DynamoDB stream is ordered CDC within a shard,
  so records are processed sequentially and processing stops at the first unsuccessful one,
  reporting that record's `SequenceNumber` as the sole `batchItemFailures` entry. Lambda
  checkpoints there and redelivers the rest - deliberately not `awssqs`'s concurrent fan-out.

## What was verified in this sandbox

This sandbox has no AWS credentials and no reachable container registry, so nothing here was
actually deployed or `docker build`-ed end to end. What *was* verified locally:

- `go test ./examples/aws-dynamodb-helloworld/...` - the app booted from its composition root
  (`newApp`), driven by a hand-built DynamoDB stream event through the real `awsdynamodb.Handler`,
  asserting on the `batchItemFailures` response (valid row, missing-id failure, unhandled change
  type - see `main_test.go`), with no network involved.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bootstrap ./examples/aws-dynamodb-helloworld`
  - the exact command the Dockerfile's build stage runs - compiles cleanly to a static ARM64
  Linux binary.
- `awsdynamodb`'s own test suite (`awsdynamodb/dynamodb_test.go`,
  `awsdynamodb/attributevalue_test.go`) exercises the binding - topic resolution, AttributeValue
  conversion, stop-at-first-failure, poison records - in isolation; this example just wires it up.

The deploy workflow's YAML was syntax-checked but has never actually run - it will start
running for real the first time you push to `main` after adding the secrets above.
