package kafka

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"
)

// flakyBuilder serves "greet" with a handler that fails its first failures calls, then succeeds.
func flakyBuilder(t *testing.T, failures int, calls *int) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	benzene.MustRegister(registry, benzene.NewTopic("greet"), func(context.Context, greetRequest) benzene.Result[greetResponse] {
		*calls++
		if *calls <= failures {
			return benzene.ServiceUnavailable[greetResponse]("downstream down")
		}
		return benzene.Ok(greetResponse{Greeting: "hi"})
	})
	return &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: benzene.NewPipeline(benzene.RouterMiddleware(registry))}
}

func TestConsumer_AtLeastOnceRedeliversInPlaceUntilSuccess(t *testing.T) {
	calls := 0
	var slept []time.Duration
	var failed int
	source := &fakeSource{messages: []kafkago.Message{topicMessage("One"), topicMessage("Two")}}
	consumer := &Consumer{
		Source:       source,
		Builder:      flakyBuilder(t, 2, &calls),
		Settlement:   envelope.Settlement{Delivery: envelope.AtLeastOnce},
		ErrorBackoff: 5 * time.Second,
		OnFailure:    func(context.Context, kafkago.Message, wire.Response) { failed++ },
		sleep:        func(_ context.Context, d time.Duration) { slept = append(slept, d) },
	}

	if err := runUntilDrained(t, consumer, source); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if calls != 4 || failed != 2 {
		t.Errorf("dispatched %d times with %d failures, want 4 and 2 (the first message retried twice)", calls, failed)
	}
	if !slices.Equal(slept, []time.Duration{5 * time.Second, 5 * time.Second}) {
		t.Errorf("backoffs = %v, want two of ErrorBackoff", slept)
	}
	if len(source.committed) != 2 || string(source.committed[0].Value) != `{"name":"One"}` {
		t.Errorf("committed %v, want both messages in order, each once", source.committed)
	}
}

func TestConsumer_AtLeastOnceShutdownLeavesTheFailedMessageUncommitted(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeSource{messages: []kafkago.Message{topicMessage("One")}, cancel: cancel}
	consumer := &Consumer{
		Source:     source,
		Builder:    flakyBuilder(t, 100, &calls),
		Settlement: envelope.Settlement{Delivery: envelope.AtLeastOnce},
		sleep:      func(context.Context, time.Duration) { cancel() }, // shutdown arrives during the backoff
	}

	if err := consumer.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v, want nil (clean shutdown)", err)
	}
	if len(source.committed) != 0 {
		t.Errorf("committed %v, want nothing: a restart must re-fetch the failed message", source.committed)
	}
}

func TestConsumer_IgnoreUnmatchedTopicsCommitsWithoutRetrying(t *testing.T) {
	source := &fakeSource{messages: []kafkago.Message{{Topic: "someone:elses", Value: []byte(`{}`)}}}
	consumer := &Consumer{
		Source:     source,
		Builder:    newTestBuilder(t),
		Settlement: envelope.Settlement{Delivery: envelope.AtLeastOnce, IgnoreUnmatchedTopics: true},
		sleep:      func(context.Context, time.Duration) { t.Error("an ignored unmatched topic was retried") },
	}

	if err := runUntilDrained(t, consumer, source); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(source.committed) != 1 {
		t.Errorf("committed %d messages, want 1", len(source.committed))
	}
}

func TestSleepContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sleepContext(ctx, time.Hour) // returns at once on a cancelled context
	sleepContext(context.Background(), time.Millisecond)
	if (&Consumer{}).errorBackoff() != time.Second {
		t.Error("errorBackoff() default is not 1s")
	}
}

// TestDefaultDeliveryMatchesTheConformanceFixture is this binding's row of transport-metadata-cases.json
// settlementDefaults (transport-bindings.md §1, obligation 8): Kafka is a "stream", and its
// skip-and-continue default must be one the stream shape allows. It lives here rather than in the
// root conformance runner because this module cannot be imported from there; it reads the root
// module's vendored snapshot by relative path, the way codegen's runner does.
func TestDefaultDeliveryMatchesTheConformanceFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "conformance", "testdata", "transport-metadata-cases.json"))
	if err != nil {
		t.Fatalf("read the vendored fixture: %v", err)
	}
	var fixture struct {
		SettlementDefaults struct {
			Shapes map[string]struct {
				OnFailure      []string `json:"onFailure"`
				UnmatchedTopic []string `json:"unmatchedTopic"`
			} `json:"shapes"`
			Bindings []struct {
				Binding string `json:"binding"`
				Shape   string `json:"shape"`
			} `json:"bindings"`
		} `json:"settlementDefaults"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}

	shape := ""
	for _, b := range fixture.SettlementDefaults.Bindings {
		if b.Binding == "kafka" {
			shape = b.Shape
		}
	}
	if shape != "stream" {
		t.Fatalf(`fixture shape for "kafka" = %q, want "stream" - the binding this port ships`, shape)
	}
	allowed := fixture.SettlementDefaults.Shapes[shape]
	if DefaultDelivery != envelope.AtMostOnce {
		t.Fatalf("DefaultDelivery = %v, which this test maps to no fixture outcome", DefaultDelivery)
	}
	// AtMostOnce on a stream is skip-and-continue, and with IgnoreUnmatchedTopics unset an unmatched
	// topic settles exactly like any other failure.
	for column, values := range map[string][]string{"onFailure": allowed.OnFailure, "unmatchedTopic": allowed.UnmatchedTopic} {
		if !slices.Contains(values, "skip-and-continue") {
			t.Errorf("%s: skip-and-continue is not among the stream shape's allowed defaults %v", column, values)
		}
	}
}
