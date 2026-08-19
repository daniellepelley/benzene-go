# Kafka Setup

The `kafka` package is the self-hosted Kafka binding: a `Consumer` loop that dispatches each
record through a Benzene pipeline (one invocation, one DI scope per record), and an outbound
`Client` that publishes wire messages. The mapping is a deliberately thin pass-through — **one
Kafka topic is one Benzene topic**, headers pass through verbatim both directions, and the message
value is the body verbatim, with no envelope wrapping.

**Worth using even if Kafka is the only transport this service ever has.** Unlike HTTP, where
`net/http` already gives you routing for free (see
[Why not just net/http?](getting-started.md#why-not-just-nethttp)), `segmentio/kafka-go`'s
`Reader.FetchMessage` on its own hands you a raw record and stops — dispatching on whatever
identifies its type, and every cross-cutting concern (validation, retries, structured logging) is
code you'd otherwise write yourself in the consume loop. `Consumer` + the middleware pipeline is
that missing layer, for Kafka specifically — the same reasoning applies to `awssqs.Consumer`,
this port's self-hosted SQS poller.

## Prerequisites

- Read [Getting started](getting-started.md) and skim the worked
  [`examples/helloworld`](../examples/helloworld) service first — this guide assumes you know how a
  handler, `Registry`, `Container`, and `Pipeline` fit together, and only shows where the Kafka
  binding slots in.
- Go 1.24+.
- A Kafka broker to run against (e.g. a local single-broker cluster on `localhost:9092`).

`kafka` is its own Go module (it depends on `github.com/segmentio/kafka-go` — a broker wire
protocol isn't hand-rollable — which the zero-dependency root module doesn't carry):

```bash
go get github.com/daniellepelley/benzene-go/kafka
```

## 1. Write the handler

Business logic is an ordinary Benzene handler. Kafka has **no response channel** — the consumer
dispatches the record and only distinguishes success from failure (via the `OnFailure` hook,
below); the handler's response value is not written back to the broker:

```go
type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	log.Printf("greeting %s", req.Name)
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}
```

The **Benzene topic must equal the Kafka topic name literally** — there is no colon-separated
topic-id convention here (unlike SQS/SNS). Whatever Kafka topic the record arrives on becomes the
Benzene topic verbatim, so register the handler under that exact name.

## 2. Build the application

Register the handler and build the pipeline. This is the ordinary three-phase composition root
([core-concepts.md §7](https://benzene.app/docs/specification/core-concepts)); the returned
`*benzene.ApplicationBuilder` is what the `Consumer` reads:

```go
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
		},
	}
}
```

## 3. Run the consumer

`kafka.Consumer` needs a `Source` (a `kafka.MessageSource`) and the `Builder`. A `*kafka.Reader`
from `segmentio/kafka-go` **constructed with a `GroupID`** satisfies `MessageSource` as-is: its
`FetchMessage` + `CommitMessages` pair is kafka-go's explicit-commit mode, which is what the
`Consumer` relies on (plain `ReadMessage` would auto-commit before the pipeline runs).

```go
func main() {
	builder := newApp().Run()

	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: []string{"localhost:9092"},
		GroupID: "greet-worker",
		Topic:   "greet",
	})
	defer reader.Close()

	consumer := &kafka.Consumer{
		Source:  reader,
		Builder: builder,
		// OnFailure is called for each record whose dispatch result is NOT a success status,
		// before that record's offset is committed. This is where a dead-letter publish or a
		// failure log belongs. Kafka has no broker-side redelivery/DLQ, so every dispatched
		// record is committed — success or not — to avoid replaying a poison message forever.
		OnFailure: func(_ context.Context, msg kafkago.Message, resp wire.Response) {
			log.Printf("dispatch failed: topic=%s status=%s body=%s",
				msg.Topic, resp.StatusCode, resp.Body)
		},
	}

	if err := consumer.Validate(); err != nil {
		log.Fatalf("consumer: %v", err)
	}

	// Run returns nil on a cancelled context (clean shutdown); a fetch/commit transport failure
	// returns that error with offsets uncommitted, so a restart resumes at-least-once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log.Println("greet-worker consuming topic \"greet\"")
	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
```

(`kafkago` is the conventional import alias for `github.com/segmentio/kafka-go`, matching the
package's own source.)

Every record gets its own pipeline invocation and DI scope, and is committed after dispatch. A
record that fails to dispatch never stops the loop — only a transport-level fetch/commit failure
returns from `Run`.

## 4. Publishing to Kafka

`kafka.NewClient(writer)` returns a `*kafka.Client` that satisfies `client.Sender`, so it composes
with `client.CorrelationDecorator` / `client.RetryDecorator` like any other sender. Give it a
`*kafka.Writer` constructed **with no fixed `Topic`** — the client sets the topic per message from
the Benzene topic (kafka-go rejects a message that sets `Topic` when the writer also has one):

```go
writer := &kafkago.Writer{Addr: kafkago.TCP("localhost:9092")}
defer writer.Close()

sender := kafka.NewClient(writer)

// Optional: derive a partition key so records sharing a key stay ordered.
sender.Key = func(topic benzene.Topic, _ []byte) []byte { return []byte(topic.String()) }

result := sender.Send(ctx, benzene.NewTopic("greet"),
	map[string]string{"x-correlation-id": "abc-123"}, []byte(`{"name":"World"}`))
// A successful publish -> accepted ("accepted for asynchronous processing"); a write failure ->
// service-unavailable.
```

The message value is the body verbatim and the headers become Kafka message headers verbatim — no
reserved header name, no envelope. The Kafka topic written to is named after the Benzene topic.

## 5. Testing

Test the handler through the pipeline with `benzenetest.Invoke` — no broker needed, and it
exercises the real middleware/DI/router path:

```go
func TestGreetHandler(t *testing.T) {
	builder := newApp().Run()

	result := benzenetest.Invoke[greetRequest, greetResponse](
		context.Background(),
		builder,
		benzene.NewTopic("greet"),
		nil,
		greetRequest{Name: "World"},
	)

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload == nil || result.Payload.Greeting != "Hello, World!" {
		t.Errorf("Payload = %+v, want greeting %q", result.Payload, "Hello, World!")
	}
}
```

To exercise the `Consumer` loop itself without a live broker, feed it a fake `kafka.MessageSource`
(the exported interface is exactly `FetchMessage` + `CommitMessages`) — this is how the package's
own `kafka_test.go` tests the consumer end to end. The outbound `Client` is testable the same way
via a fake `kafka.MessageWriter`.

## Troubleshooting

- **Handler never fires** — the registered Benzene topic must equal the literal Kafka topic name;
  there's no colon-separated topic id. Confirm the `ReaderConfig.Topic` and the `benzene.NewTopic`
  value match exactly, and that the consumer group's `GroupID` isn't already parked past the
  offsets you expect.
- **`kafka: Consumer requires both Source and Builder`** — `Validate()` (or the first `Run` fetch)
  found a nil `Source` or `Builder`. Construct the reader and run the app before building the
  `Consumer`.
- **`Topic must not be specified for both Writer and Message`** — your `*kafka.Writer` was
  constructed with a `Topic`; leave it empty and let the `Client` set it per message.
- **Failures seem to disappear** — with a nil `OnFailure`, non-success dispatch results are simply
  committed past. Set an `OnFailure` (log or dead-letter) unless your pipeline middleware already
  records failures.

## See Also

- [Quickstart](../README.md#quickstart)
- [`examples/helloworld`](../examples/helloworld)
- [Wire contracts](https://benzene.app/docs/specification/wire-contracts)
- [Core concepts](https://benzene.app/docs/specification/core-concepts)
