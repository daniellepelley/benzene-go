# kafka-helloworld

The greet handler driven by a Kafka consumer group via the `kafka` package, with an outbound
`kafka.Client` for the publish side - the Go counterpart of the .NET repo's `examples/Kafka` (a
worker consuming a topic + a producer).

## Run it

Needs a Kafka broker (`KAFKA_BROKERS`, default `localhost:9092`) with a `greet` topic:

```
KAFKA_BROKERS=localhost:9092 go run ./examples/kafka-helloworld
```

Publish a record to the `greet` topic with any Kafka client (`{"name":"World"}`) and watch it get
handled. There is no HTTP endpoint - this is the queue-consumer shape, like
`examples/gcp-pubsub-helloworld`.

## What this demonstrates

- **A Kafka consumer that hosts a Benzene handler**: `kafka.Consumer` fetches from a consumer group,
  runs each record through the pipeline (its own DI scope per record), and commits - one Kafka topic
  maps to one Benzene topic, headers pass through verbatim, no envelope wrapping.
- **The failure hook**: Kafka has no broker-side redelivery or dead-letter queue, so a non-success
  dispatch goes to `Consumer.OnFailure` (a dead-letter publish or log belongs there) and is
  committed past rather than replayed forever.
- **The outbound client**: `kafka.NewClient(writer).Send(...)` publishes to the Kafka topic named
  after the Benzene topic, mapping a successful write to `accepted`.
- **A port interface** (`Greeter`) resolved from the DI scope. A Kafka consumer has no response
  channel, so the handler's "output" is a side effect through the port - here recording the greeting.

## Module

This is its own Go module (`go.mod`) - it depends on the `kafka` module (which needs
`github.com/segmentio/kafka-go`), so it can't live in the zero-dependency root module. `go.work` ties
it together for local development. See `main_test.go` for the `benzenetest`-driven component test: a
fake `kafka.MessageSource` feeds a record into the real `Consumer`, and a spy `Greeter` captures what
the handler did - no live broker needed.
