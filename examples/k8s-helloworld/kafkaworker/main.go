// Command kafkaworker is the Kafka leg of this example: a long-running pod consuming one topic,
// dispatching every record into the same greeting.Handler the api pod exposes over HTTP and
// sqsworker exposes over SQS.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/kafka"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/daniellepelley/benzene-go/examples/k8s-helloworld/greeting"
)

func newApp() *benzene.ApplicationBuilder {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greeting.GreetRequest, greeting.GreetResponse](greeting.Handler)); err != nil {
		log.Fatalf("register greet handler: %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func brokersFromEnv() []string {
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		return []string{brokers}
	}
	return []string{"localhost:9092"}
}

func topicFromEnv() string {
	if topic := os.Getenv("KAFKA_CONSUME_TOPIC"); topic != "" {
		return topic
	}
	return "greet"
}

func main() {
	brokers := brokersFromEnv()
	topic := topicFromEnv()

	// Explicit-commit reader (a GroupID makes FetchMessage/CommitMessages the explicit-commit mode
	// the kafka package needs - Reader.ReadMessage would auto-commit before the pipeline runs).
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: brokers,
		GroupID: "k8s-helloworld",
		Topic:   topic,
	})
	defer reader.Close()

	consumer := &kafka.Consumer{
		Source:  reader,
		Builder: newApp(),
		OnFailure: func(_ context.Context, msg kafkago.Message, resp wire.Response) {
			log.Printf("greet failed [%s]: %s", resp.StatusCode, string(msg.Value))
		},
	}
	if err := consumer.Validate(); err != nil {
		log.Fatalf("consumer: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("k8s-helloworld kafkaworker consuming %q from %v", topic, brokers)
	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
