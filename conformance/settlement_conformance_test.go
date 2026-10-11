package conformance

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// transport-metadata-cases.json's settlementDefaults (transport-bindings.md §1, obligation 8) pins,
// per binding, its shape and the defaults that shape allows. This port's one-way bindings all settle
// through envelope.Settlement, whose zero value is the default every binding ships with except where
// the binding documents otherwise; settlementBindings is this port's claim of which fixture binding
// each Go binding is, and with which default.

// outcome is the fixture's vocabulary for what a binding does by default with a failed message.
type outcome string

const (
	redeliver       outcome = "redeliver"
	skipAndContinue outcome = "skip-and-continue"
	notApplicable   outcome = "n/a"
)

// goBinding is one binding this port ships, mapped to the fixture.
type goBinding struct {
	name      string  // the Go entry point
	fixtureID string  // the fixture's binding id; "" when the fixture names none (reported, not invented)
	shape     string  // this port's claim of the binding's shape
	onFailure outcome // the binding's default for a failed (or unmatched) message
	// assertedIn names the module whose own test asserts the binding's default constant, when the
	// binding lives outside the root module and this runner cannot import it.
	assertedIn string
}

// zeroSettlementOutcome is what the zero envelope.Settlement does with a failed message, measured by
// dispatching one rather than assumed: every binding below whose default is "the zero Settlement"
// gets this value.
func zeroSettlementOutcome(t *testing.T) outcome {
	t.Helper()
	registry := benzene.NewRegistry()
	got := (envelope.Settlement{}).Dispatch(context.Background(), benzene.NewPipeline(benzene.RouterMiddleware(registry)),
		benzene.NewContainer(), wire.Request{Topic: "conformance:no-such-topic"})
	if got.Acknowledge {
		return skipAndContinue
	}
	return redeliver
}

func settlementBindings(zero outcome) []goBinding {
	return []goBinding{
		// Queues: one message, one settlement.
		{"awssqs.Handler", "sqs", "queue", zero, "awssqs"},
		{"awssqs.Consumer", "sqs", "queue", zero, "awssqs"},
		{"rabbitmq.Consumer", "rabbitmq", "queue", zero, "rabbitmq"},
		{"gcppubsub.Handler", "pubsub", "queue", zero, ""},
		{"azurefunctions.QueueHandler (Service Bus trigger)", "servicebus", "queue", zero, ""},
		{"azurefunctions.QueueHandler (Storage Queue trigger)", "queue-storage", "queue", zero, ""},
		{"azureservicebus.Worker", "servicebus", "queue", zero, "azureservicebus"},
		// One-shot: a single delivery with no per-message ack.
		{"awssns.Handler", "sns", "one-shot", zero, "awssns"},
		{"awseventbridge.Handler", "eventbridge", "one-shot", zero, "awseventbridge"},
		{"awss3.Handler", "s3", "one-shot", zero, ""},
		{"azurefunctions.EventGridHandler", "event-grid", "one-shot", zero, ""},
		{"azurefunctions.TimerHandler", "timer", "one-shot", zero, ""},
		{"awslambda.AsyncEnvelopeHandler", "envelope-async", "one-shot", zero, ""},
		// Streams: redeliver by default, except the self-hosted Kafka consumer, which documents
		// skip-and-continue (kafka.DefaultDelivery, asserted against this fixture in its own module).
		{"kafka.Consumer", "kafka", "stream", skipAndContinue, "kafka"},
		{"awskafka.Handler", "kafka", "stream", zero, ""},
		{"awskinesis.Handler", "kinesis", "stream", zero, ""},
		{"awsdynamodb.Handler", "dynamodb-streams", "stream", zero, ""},
		{"azurefunctions.EventHubHandler", "eventhub", "stream", zero, ""},
		{"azureeventhub.Consumer", "eventhub", "stream", zero, "azureeventhub"},
		{"azurefunctions.CosmosHandler", "cosmosdb-changefeed", "stream", zero, ""},
		{"azurecosmos.Worker", "cosmosdb-changefeed", "stream", zero, "azurecosmos"},
		// Request-response: settled by responding.
		{"httpbinding.Handler", "http", "request-response", notApplicable, ""},
		{"awslambda.HTTPHandler", "http", "request-response", notApplicable, ""},
		{"azurefunctions.Handler", "http", "request-response", notApplicable, ""},
		{"gcpfunctions.RegisterHTTP", "http", "request-response", notApplicable, "gcpfunctions"},
		{"grpcbinding.UnaryServerInterceptor", "grpc", "request-response", notApplicable, "grpcbinding"},
		{"httpbinding.EnvelopeHandler", "envelope", "request-response", notApplicable, ""},
		{"awslambda.EnvelopeHandler", "envelope", "request-response", notApplicable, ""},
		// CloudEvents: a push delivery or a CloudEvent-triggered function, which the sender retries.
		{"cloudevents.Handler (CloudEvents over HTTP push)", "cloudevents", "one-shot", zero, ""},
		{"gcpfunctions.RegisterCloudEvent (Eventarc/Pub/Sub CloudEvent function)", "cloudevents", "one-shot", zero, "gcpfunctions"},
	}
}

func TestConformance_SettlementDefaults(t *testing.T) {
	var fixture struct {
		SettlementDefaults struct {
			Shapes map[string]struct {
				OnFailure      []outcome `json:"onFailure"`
				UnmatchedTopic []outcome `json:"unmatchedTopic"`
			} `json:"shapes"`
			Bindings []struct {
				Binding string `json:"binding"`
				Shape   string `json:"shape"`
			} `json:"bindings"`
		} `json:"settlementDefaults"`
	}
	loadFixture(t, "transport-metadata-cases.json", &fixture)
	requireCases(t, len(fixture.SettlementDefaults.Bindings), "transport-metadata-cases", "settlementDefaults.bindings")

	shapeOf := make(map[string]string, len(fixture.SettlementDefaults.Bindings))
	for _, b := range fixture.SettlementDefaults.Bindings {
		shapeOf[b.Binding] = b.Shape
	}

	zero := zeroSettlementOutcome(t)
	if zero != redeliver {
		t.Fatalf("the zero envelope.Settlement %s a failed message, want redeliver - every queue and one-shot binding relies on it", zero)
	}

	mapped := map[string]bool{}
	for _, b := range settlementBindings(zero) {
		t.Run(b.name, func(t *testing.T) {
			if b.fixtureID == "" {
				t.Skipf("settlementDefaults names no binding for %s (default %s); reported to the spec owners rather than added here", b.name, b.onFailure)
			}
			mapped[b.fixtureID] = true
			shape, ok := shapeOf[b.fixtureID]
			if !ok {
				t.Fatalf("settlementDefaults has no binding %q", b.fixtureID)
			}
			if shape != b.shape {
				t.Errorf("shape = %q, want the fixture's %q", b.shape, shape)
			}
			allowed := fixture.SettlementDefaults.Shapes[shape]
			// With IgnoreUnmatchedTopics unset, an unmatched topic settles exactly like any other
			// failure, so one default is checked against both columns.
			if !contains(allowed.OnFailure, b.onFailure) {
				t.Errorf("onFailure default %s is not among %v allowed for a %s binding", b.onFailure, allowed.OnFailure, shape)
			}
			if !contains(allowed.UnmatchedTopic, b.onFailure) {
				t.Errorf("unmatchedTopic default %s is not among %v allowed for a %s binding", b.onFailure, allowed.UnmatchedTopic, shape)
			}
		})
	}

	// Fixture bindings this port does not ship, with the reason.
	notShipped := map[string]string{
		"blob-storage": "the Azure Functions Blob trigger is deferred (SDK-typed; see ROADMAP.md)",
	}
	for _, b := range fixture.SettlementDefaults.Bindings {
		if !mapped[b.Binding] {
			if reason, ok := notShipped[b.Binding]; ok {
				t.Logf("settlementDefaults binding %q not shipped: %s", b.Binding, reason)
				continue
			}
			t.Errorf("settlementDefaults names %q, which no Go binding maps to - map it, or record why it is not shipped", b.Binding)
		}
	}
}

func contains(values []outcome, v outcome) bool {
	for _, value := range values {
		if value == v {
			return true
		}
	}
	return false
}
