// Command k8s-helloworld hosts the shared greeting.Handler three ways - over net/http, an SQS
// poller, and a Kafka consumer - in one process. Each leg runs in its own goroutine against a
// shared context.Context, cancelled together on SIGINT/SIGTERM: Go has no "generic host" that
// starts things sequentially the way .NET's does, so there's no equivalent of that port's
// hosted-service-starvation gotcha (see docs/getting-started-kubernetes.md) - a blocking
// awssqs.Consumer.Run alongside a net/http server just works, as two goroutines the runtime
// schedules independently.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/kafka"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/daniellepelley/benzene-go/examples/k8s-helloworld/greeting"
)

// newApp is the composition root every leg below shares - the same registered handler, reached
// three ways.
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

func portFromEnv() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func brokersFromEnv() []string {
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		return []string{brokers}
	}
	return []string{"localhost:9092"}
}

func newSqsConsumer(ctx context.Context) (*awssqs.Consumer, error) {
	queueURL := os.Getenv("QUEUE_URL")
	if queueURL == "" {
		return nil, fmt.Errorf("QUEUE_URL environment variable is not set")
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	// SQS_ENDPOINT_URL is only set for a LocalStack/emulator run (see compose/docker-compose.yml);
	// unset (the real-AWS/EKS case), sqs.NewFromConfig uses the SDK's default endpoint resolution -
	// this leg never prescribes how you authenticate, same principle as every other Benzene AWS
	// client on Kubernetes (an IRSA role bound to the pod's ServiceAccount).
	sqsClient := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if endpoint := os.Getenv("SQS_ENDPOINT_URL"); endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})

	consumer := &awssqs.Consumer{
		API:      sqsClient,
		QueueURL: queueURL,
		Builder:  newApp(),
		OnFailure: func(messageID string, resp wire.Response) {
			log.Printf("greet failed [%s]: %s", resp.StatusCode, messageID)
		},
	}
	if err := consumer.Validate(); err != nil {
		return nil, fmt.Errorf("sqs consumer: %w", err)
	}
	return consumer, nil
}

func newKafkaConsumer() (*kafka.Consumer, func(), error) {
	brokers := brokersFromEnv()

	// Explicit-commit reader (a GroupID makes FetchMessage/CommitMessages the explicit-commit mode
	// the kafka package needs - Reader.ReadMessage would auto-commit before the pipeline runs).
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: brokers,
		GroupID: "k8s-helloworld",
		Topic:   "greet",
	})

	consumer := &kafka.Consumer{
		Source:  reader,
		Builder: newApp(),
		OnFailure: func(_ context.Context, msg kafkago.Message, resp wire.Response) {
			log.Printf("greet failed [%s]: %s", resp.StatusCode, string(msg.Value))
		},
	}
	if err := consumer.Validate(); err != nil {
		reader.Close()
		return nil, nil, fmt.Errorf("kafka consumer: %w", err)
	}
	return consumer, func() { reader.Close() }, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sqsConsumer, err := newSqsConsumer(ctx)
	if err != nil {
		log.Fatalf("sqs: %v", err)
	}

	kafkaConsumer, closeKafka, err := newKafkaConsumer()
	if err != nil {
		log.Fatalf("kafka: %v", err)
	}
	defer closeKafka()

	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
	server := &http.Server{
		Addr:              ":" + portFromEnv(),
		Handler:           httpbinding.Handler(newApp(), routes),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Each leg runs in its own goroutine against the shared ctx, cancelled together on
	// SIGINT/SIGTERM. errCh carries the first fatal error from any leg so the process exits
	// non-zero (and Kubernetes restarts the pod) rather than limping along with one transport
	// silently dead.
	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("k8s-helloworld http listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Print("k8s-helloworld sqs polling")
		if err := sqsConsumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- fmt.Errorf("sqs: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Print("k8s-helloworld kafka consuming \"greet\"")
		if err := kafkaConsumer.Run(ctx); err != nil {
			errCh <- fmt.Errorf("kafka: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Print("shutting down")
	case err := <-errCh:
		log.Printf("leg failed, shutting down the rest: %v", err)
		stop() // cancels ctx, so the sqs/kafka legs unwind too
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	wg.Wait()
	log.Print("k8s-helloworld stopped")
}
