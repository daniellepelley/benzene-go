# aws-kafka-helloworld

A Kafka **consumer** Lambda. There is no publisher: the "publish" is producing a record to an
`orders` Kafka topic, and an Amazon MSK (or self-managed-Kafka) event source mapping turns each
record into an invocation this Lambda consumes.

```
produce to `orders` topic --> MSK/Kafka event source mapping --> consumer Lambda --> orders handler
```

The Kafka topic **is** the Benzene topic, verbatim — one Kafka topic maps to one Benzene topic (no
envelope, no header). The record value is base64-decoded back to the JSON the producer wrote, and
Kafka headers pass through verbatim, before the handler sees it.

> **Not the self-hosted `kafka` module.** This example uses `awskafka`, the zero-dependency adapter
> for AWS's *managed* event source mapping (AWS delivers the records as plain JSON, so there's no
> SDK). The repo's `kafka` module is different: it runs its own broker consumer loop against a
> cluster you operate, and needs `segmentio/kafka-go`. Use this one when Lambda is your compute and
> MSK/Kafka is your trigger; use `kafka` when you host your own long-running consumer.

## Layout

Like `aws-kinesis-helloworld` (and unlike `aws-sqs-helloworld`), this example is **not** its own Go
module — it lives in the root `benzene-go` module, because the `awskafka` binding it uses is itself
zero-dependency. The whole example is one `main.go`.

## Deploy

Requires [AWS SAM CLI](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html)
and Docker, with AWS credentials configured — **and an existing MSK cluster**.

Unlike the Kinesis example, this template does **not** create the message source: an MSK cluster
needs a VPC, subnets, security groups, and broker provisioning, which is too much to stand up (and
verify) inline. Pass the ARN of a cluster you already have:

```
cd examples/aws-kafka-helloworld
sam build
sam deploy --guided --parameter-overrides MskClusterArn=arn:aws:kafka:REGION:ACCOUNT:cluster/NAME/UUID
```

That wires the consumer Lambda to the cluster's `orders` topic. The mapping reports partial batch
failures as `{partition, offset}` objects (the Kafka shape), so AWS resumes a partition from the
failing offset rather than committing past it — the binding always emits that shape, and MSK
mappings read it without any extra `FunctionResponseTypes` toggle.

### Self-managed Kafka

For a Kafka cluster you run yourself (not MSK), change the event in `template.yaml` from `Type: MSK`
to `Type: SelfManagedKafka`, drop `Stream`, and add `KafkaBootstrapServers` (and the auth/VPC
properties your cluster needs). The Go code and the `awskafka` binding are identical — only the
event source mapping's declaration differs.

## CI/CD

`.github/workflows/deploy-aws-kafka-helloworld.yml` runs the same `sam build && sam deploy` on every
push to `main` that touches this example (or a package it depends on). It's gated on **both**
`secrets.AWS_ACCESS_KEY_ID` and `vars.MSK_CLUSTER_ARN` being set — the job is **skipped** (not
failed) until you configure the same AWS secrets as `aws-lambda-helloworld` (see its README:
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optionally `AWS_REGION`) **and** an `MSK_CLUSTER_ARN`
repository variable pointing at your cluster. Without a cluster to point at, there is nothing to
deploy against.

## Try it

Produce a record to the `orders` topic with any Kafka producer (from a client inside the cluster's
VPC), e.g. the console producer:

```
echo '{"id":"o-1","item":"widget","amount":9.99}' | \
  kafka-console-producer --bootstrap-server "$BROKERS" --topic orders
```

There's no synchronous response — that's the nature of stream processing. To see the result, check
the consumer function's CloudWatch logs (`order received: id=o-1 item="widget" amount=9.99`). A
record with no `id` (which `orderReceived` rejects) shows up there as a failed batch item — reported
by its `{partition, offset}` for redelivery, not as an error back to the producer.

## What this demonstrates

- **`awskafka.Handler`**: adapts the Lambda MSK/Kafka event source mapping's batch payload —
  resolving the topic as the Kafka topic verbatim, base64-decoding the record value into the
  producer's JSON so the handler deserializes an ordinary struct, passing Kafka headers through
  verbatim, and running each record through the pipeline with its own DI scope.
- **Per-partition, ordered, stop-at-first-failure batching**: records are ordered within a
  topic-partition, so each partition is processed sequentially and stops at its first unsuccessful
  record, reporting that partition's `{partition, offset}` as a `batchItemFailures` entry. Partitions
  are independent (a failure in one does not stop another). AWS resumes each partition from its
  reported offset. This is the object-shaped failure identifier the Kafka/MSK mapping reads — unlike
  the string sequence number of `aws-kinesis-helloworld`/`aws-dynamodb-helloworld`.

## What was verified in this sandbox

This sandbox has no AWS credentials, no MSK cluster, and no reachable container registry, so nothing
here was actually deployed or `docker build`-ed end to end. What *was* verified locally:

- `go test ./examples/aws-kafka-helloworld/...` — the app booted from its composition root
  (`newApp`), driven by a native Kafka record through `benzenetest.SendKafkaEvent`, asserting on the
  `batchItemFailures` response (valid record, missing-id failure, unrouted topic), with no network
  involved.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bootstrap ./examples/aws-kafka-helloworld` —
  the exact command the Dockerfile's build stage runs — compiles cleanly to a static ARM64 Linux
  binary.
- `awskafka`'s own test suite (`awskafka/kafka_test.go`) exercises the binding — topic/body/header
  resolution, base64 decode, per-partition stop-at-first-failure, poison records — in isolation;
  this example just wires it up.

The deploy workflow's YAML was syntax-checked but has never actually run — it will start running for
real the first time you push to `main` after adding the secrets/variables above (and having an MSK
cluster to deploy against). The `MSK`/`SelfManagedKafka` event source mapping's runtime behavior is
based on AWS's documented contract, not an observed deploy.
