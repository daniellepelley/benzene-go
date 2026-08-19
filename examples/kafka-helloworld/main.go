// Command kafka-helloworld is the Kafka analogue of examples/helloworld: the same greet handler
// behind a port interface, driven this time by a Kafka consumer group via the kafka package, with
// an outbound kafka.Client for the publish side. It is the Go counterpart of the .NET repo's
// examples/Kafka (a worker consuming a topic + a producer).
//
// Kafka has no response channel - result mapping is acknowledge/log only (see the kafka package
// doc) - so the "output" of a greeting is a side effect: greetHandler asks its Greeter port to
// record the greeting. In a real service that adapter would write to a database, emit a downstream
// event, etc.; here it is an in-memory log the test asserts against. This is the queue-consumer
// shape, the same as examples/gcp-pubsub-helloworld, not the request/response HTTP shape.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/kafka"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"
)

// Greeter is the "port": greetHandler depends on this interface, not on how a greeting is recorded.
// Swapping recordingGreeter for a database- or event-backed adapter needs no change to the handler.
type Greeter interface {
	Greet(name string) string
}

// recordingGreeter is the adapter used by this example: it produces the greeting and keeps every
// one it has produced, since a Kafka consumer has no response to return.
type recordingGreeter struct {
	mu      sync.Mutex
	greeted []string
}

func (g *recordingGreeter) Greet(name string) string {
	greeting := "Hello, " + name + "!"
	g.mu.Lock()
	g.greeted = append(g.greeted, greeting)
	g.mu.Unlock()
	return greeting
}

// greeterKey is the typed DI key for the Greeter dependency - a struct key can't collide with
// another package's key, unlike a bare string (see helloworld's main.go for the rationale).
type greeterKey struct{}

// greetTopic is both the Benzene topic and (per the kafka package's one-Kafka-topic-per-Benzene-topic
// mapping) the Kafka topic name the consumer subscribes to and the client publishes to.
const greetTopic = "greet"

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

// greetHandler resolves its Greeter via ScopeFromContext (see helloworld's main.go) and records the
// greeting. Its result status still matters: a non-success status routes the message to the
// Consumer's OnFailure hook before the offset is committed.
func greetHandler(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[greetResponse]("no DI scope on context")
	}
	greeter := benzene.GetService[Greeter](scope, greeterKey{})
	return benzene.Ok(greetResponse{Greeting: greeter.Greet(req.Name)})
}

// newApp is the composition root: the three-phase benzene.App both main() and the test boot from.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.AddSingleton(container, greeterKey{}, func(_ *benzene.Scope) Greeter { return &recordingGreeter{} })
			benzene.MustRegister(registry, benzene.NewTopic(greetTopic), greetHandler)
		},
	}
}

// newConsumer wires the Kafka consumer over source, dispatching each record through the app's
// pipeline. OnFailure is where a dead-letter publish or failure log belongs (Kafka has no
// broker-side redelivery to hand a failed message to) - here it just logs.
func newConsumer(builder *benzene.ApplicationBuilder, source kafka.MessageSource) *kafka.Consumer {
	return &kafka.Consumer{
		Source:  source,
		Builder: builder,
		OnFailure: func(_ context.Context, msg kafkago.Message, resp wire.Response) {
			log.Printf("greet failed [%s]: %s", resp.StatusCode, string(msg.Value))
		},
	}
}

func brokersFromEnv() []string {
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		return []string{brokers}
	}
	return []string{"localhost:9092"}
}

func main() {
	builder := newApp().Run()
	brokers := brokersFromEnv()

	// Explicit-commit reader (a GroupID makes FetchMessage/CommitMessages the explicit-commit mode
	// the kafka package needs - Reader.ReadMessage would auto-commit before the pipeline runs).
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: brokers,
		GroupID: "kafka-helloworld",
		Topic:   greetTopic,
	})
	defer reader.Close()

	consumer := newConsumer(builder, reader)
	if err := consumer.Validate(); err != nil {
		log.Fatalf("consumer: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("kafka-helloworld consuming %q from %v", greetTopic, brokers)
	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
