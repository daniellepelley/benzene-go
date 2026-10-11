package conformance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awsdynamodb"
	"github.com/daniellepelley/benzene-go/awskafka"
	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/gcppubsub"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
	"github.com/daniellepelley/benzene-go/wire"
)

// transport-metadata-cases.json pins the native-metadata topic resolution of wire-contracts.md §2:
// the reserved `topic` key (tier A, configurable), the case-insensitive read, the
// remaining-metadata-becomes-headers rule with the consumed key excluded, the "only the configured
// key routes" guard, the version travelling beside the topic, and an overridden key being honoured.
//
// Three groups run here, and the same metadataCases + overrideCases run again in each binding
// module that this module cannot import:
//
//   - topicSources: every fixture binding is accounted for in topicSourceClaims - which Go binding
//     ships it and where its topic comes from - and the root-module bindings are driven to show it.
//   - metadataCases and overrideCases, through the shared wire.ResolveMetadataTopic primitive and
//     then through every native-metadata binding in this module (gcppubsub, the azurefunctions
//     Service Bus and Event Hub triggers) via internal/transportmetadata, each delivering a real
//     message on its native channel to a builder configured with the case's reserved names.
//   - The binding modules (awssqs, awssns, azureservicebus, azureeventhub, rabbitmq) run the same
//     two groups through their own entry points in their own transport_metadata_conformance_test.go.

func transportMetadataFixturePath() string { return "testdata/" + transportmetadata.FixtureName }

type transportMetadataFixture struct {
	DefaultMetadataKeys struct {
		Topic   string `json:"topic"`
		Version string `json:"version"`
		Headers string `json:"headers"`
	} `json:"defaultMetadataKeys"`
	TopicSources []topicSource `json:"topicSources"`
}

type topicSource struct {
	Binding       string `json:"binding"`
	Source        string `json:"source"`
	NativeChannel string `json:"nativeChannel"`
	NativeField   string `json:"nativeField"`
	Pattern       string `json:"pattern"`
	Fallback      string `json:"fallback"`
}

func TestConformance_TransportMetadata(t *testing.T) {
	var fixture transportMetadataFixture
	loadFixture(t, "transport-metadata-cases.json", &fixture)

	// The port's own constants, compared against the fixture's names - the cheap check that a
	// renamed or misspelled default cannot pass. The resolution below then runs under the port's
	// constants, not the fixture's, so it exercises what a service actually ships with.
	if wire.DefaultTopicKey != fixture.DefaultMetadataKeys.Topic {
		t.Errorf("wire.DefaultTopicKey = %q, want %q", wire.DefaultTopicKey, fixture.DefaultMetadataKeys.Topic)
	}
	if wire.DefaultVersionKey != fixture.DefaultMetadataKeys.Version {
		t.Errorf("wire.DefaultVersionKey = %q, want %q", wire.DefaultVersionKey, fixture.DefaultMetadataKeys.Version)
	}
	// The EventBridge embedded-headers key (awseventbridge.EmbeddedHeadersKey is this constant).
	if wire.EmbeddedHeadersKey != fixture.DefaultMetadataKeys.Headers {
		t.Errorf("wire.EmbeddedHeadersKey = %q, want %q", wire.EmbeddedHeadersKey, fixture.DefaultMetadataKeys.Headers)
	}

	// The shared primitive every native-metadata binding delegates to. The version is read the way
	// RouterMiddleware reads it, off the remaining headers.
	for _, c := range transportmetadata.Load(t, transportMetadataFixturePath()) {
		t.Run("wire.ResolveMetadataTopic/"+c.Name, func(t *testing.T) {
			names := c.Names()
			topic, headers := wire.ResolveMetadataTopic(c.Metadata, names.Topic())
			transportmetadata.Assert(t, transportmetadata.Delivery{
				Topic: topic, Headers: headers, Version: wire.ResolveVersion(headers, names.Version()),
			}, c.Expected)
		})
	}
}

// TestConformance_TransportMetadataBindings runs metadataCases and overrideCases through every
// native-metadata inbound binding in this module.
func TestConformance_TransportMetadataBindings(t *testing.T) {
	probes := map[string]transportmetadata.Probe{
		"gcppubsub.Handler": probePubSub,
		"azurefunctions.QueueHandler (Service Bus trigger)":  probeAzureServiceBusTrigger,
		"azurefunctions.EventHubHandler (Event Hub trigger)": probeAzureEventHubTrigger,
	}
	for name, probe := range probes {
		t.Run(name, func(t *testing.T) {
			transportmetadata.Run(t, transportMetadataFixturePath(), probe)
		})
	}
}

// probePubSub delivers a push-subscription message with the metadata as its attributes.
func probePubSub(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
	t.Helper()
	push := map[string]any{
		"message": map[string]any{
			"data":       base64.StdEncoding.EncodeToString([]byte(transportmetadata.Body)),
			"attributes": metadata,
		},
		"subscription": "projects/conformance/subscriptions/conformance",
	}
	serveJSON(t, gcppubsub.Handler(builder), push)
}

// probeAzureServiceBusTrigger delivers a Service Bus trigger invocation with the metadata as the
// message's application properties (the host's Metadata.UserProperties).
func probeAzureServiceBusTrigger(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
	t.Helper()
	invocation := map[string]any{
		"Data":     map[string]any{"message": transportmetadata.Body},
		"Metadata": map[string]any{"UserProperties": metadata},
	}
	serveJSON(t, azurefunctions.QueueHandler(builder, "message"), invocation)
}

// probeAzureEventHubTrigger delivers a one-event Event Hub trigger batch with the metadata as the
// event's application properties (the host's Metadata.PropertiesArray[0]).
func probeAzureEventHubTrigger(t *testing.T, builder *benzene.ApplicationBuilder, metadata map[string]string) {
	t.Helper()
	invocation := map[string]any{
		"Data":     map[string]any{"events": []string{transportmetadata.Body}},
		"Metadata": map[string]any{"PropertiesArray": []map[string]string{metadata}},
	}
	serveJSON(t, azurefunctions.EventHubHandler(builder, "events"), invocation)
}

func serveJSON(t *testing.T, handler http.Handler, body any) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal delivery: %v", err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(data))))
}

// topicSourceClaim is this port's claim for one topicSources entry: the Go bindings that ship it
// and where their topic comes from. assertedIn names the module whose own test drives a binding
// this module cannot import; "" means it is driven here.
type topicSourceClaim struct {
	goBindings []string
	source     string
	assertedIn string
}

// topicSourceClaims is every topicSources entry, keyed by fixture binding id. A fixture entry with
// no claim fails the run: a binding this port does not ship is claimed with no goBindings.
var topicSourceClaims = map[string]topicSourceClaim{
	"sqs":              {[]string{"awssqs.Handler", "awssqs.Consumer"}, "metadata", "awssqs"},
	"sns":              {[]string{"awssns.Handler"}, "metadata", "awssns"},
	"pubsub":           {[]string{"gcppubsub.Handler"}, "metadata", ""},
	"servicebus":       {[]string{"azurefunctions.QueueHandler (Service Bus trigger)", "azureservicebus.Worker"}, "metadata", "azureservicebus"},
	"eventhub":         {[]string{"azurefunctions.EventHubHandler", "azureeventhub.Consumer"}, "metadata", "azureeventhub"},
	"kafka":            {[]string{"awskafka.Handler", "kafka.Consumer"}, "native", "kafka"},
	"rabbitmq":         {[]string{"rabbitmq.Consumer"}, "metadata", "rabbitmq"},
	"eventbridge":      {[]string{"awseventbridge.Handler"}, "native", "awseventbridge"},
	"dynamodb-streams": {[]string{"awsdynamodb.Handler"}, "derived", ""},
	"http":             {[]string{"httpbinding.Handler", "awslambda.HTTPHandler", "azurefunctions.Handler"}, "route", ""},
	"grpc":             {[]string{"grpcbinding.UnaryServerInterceptor"}, "method", "grpcbinding"},
}

// topicSourceDivergences are claims that disagree with the fixture, each with the reason it is not
// fixed here. An entry is an open question for the spec, not a waiver: it must be resolved there or
// here, and this runner fails once the claim and the fixture agree again so the entry cannot
// outlive it.
var topicSourceDivergences = map[string]string{
	// The fixture (with wire-contracts §2 and transport-bindings' Kafka entry) says a Kafka consumer
	// reads the topic from the `topic` header. This port's two Kafka bindings - like .NET's
	// KafkaMessageTopicGetter (the reference) and TypeScript's - route on the Kafka topic itself, and
	// their Kafka clients produce to a Kafka topic named after the Benzene topic with no `topic`
	// header; Python follows the fixture (one Kafka topic, header-routed). The ports therefore do not
	// interoperate over Kafka today, and which side moves is a spec decision, not a fix in one port:
	// raised against the spec rather than changed here.
	"kafka": "routes on the Kafka topic itself, as .NET and TypeScript do; the fixture (and Python) read the `topic` header",
}

// TestConformance_TransportMetadataTopicSources runs the topicSources group: every fixture binding
// is claimed, each claim matches the fixture's source (or is a listed divergence), and the
// root-module bindings whose source is not native metadata are driven to show where their topic
// comes from. The metadata-sourced ones are driven by TestConformance_TransportMetadataBindings.
func TestConformance_TransportMetadataTopicSources(t *testing.T) {
	var fixture transportMetadataFixture
	loadFixture(t, "transport-metadata-cases.json", &fixture)
	requireCases(t, len(fixture.TopicSources), "transport-metadata-cases", "topicSources")

	seen := map[string]bool{}
	for _, entry := range fixture.TopicSources {
		seen[entry.Binding] = true
		claim, ok := topicSourceClaims[entry.Binding]
		if !ok {
			t.Errorf("topicSources %q has no claim in topicSourceClaims - say which Go binding ships it, or that none does", entry.Binding)
			continue
		}
		reason, divergent := topicSourceDivergences[entry.Binding]
		switch {
		case claim.source != entry.Source && !divergent:
			t.Errorf("topicSources %q: this port's source is %q, the fixture's %q", entry.Binding, claim.source, entry.Source)
		case claim.source == entry.Source && divergent:
			t.Errorf("topicSources %q is listed as a divergence (%q) but the claim now matches the fixture - drop the entry", entry.Binding, reason)
		case divergent:
			t.Logf("topicSources %q: DIVERGES from the fixture (fixture %q, this port %q): %s", entry.Binding, entry.Source, claim.source, reason)
		}
	}
	for binding := range topicSourceClaims {
		if !seen[binding] {
			t.Errorf("topicSourceClaims names %q, which the fixture's topicSources does not - drop it", binding)
		}
	}
	for binding := range topicSourceDivergences {
		if !seen[binding] {
			t.Errorf("topicSourceDivergences names %q, which the fixture's topicSources does not - drop it", binding)
		}
	}

	sources := map[string]topicSource{}
	for _, entry := range fixture.TopicSources {
		sources[entry.Binding] = entry
	}

	t.Run("dynamodb-streams/awsdynamodb.Handler", func(t *testing.T) {
		pattern := sources["dynamodb-streams"].Pattern
		if pattern != "{tableName}:{eventName}" {
			t.Fatalf("dynamodb-streams pattern = %q, this runner substitutes {tableName}:{eventName}", pattern)
		}
		builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
		event := benzenetest.NewDynamoDBStreamEvent(t, "INSERT", "orders", "1", map[string]any{"id": "o-1"})
		if _, err := awsdynamodb.Handler(builder)(context.Background(), event); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		want := strings.NewReplacer("{tableName}", "orders", "{eventName}", "INSERT").Replace(pattern)
		if got := recorder.Only(t).Topic; got != want {
			t.Errorf("topic = %q, want %q (derived from the change event)", got, want)
		}
	})

	t.Run("http/httpbinding.Handler", func(t *testing.T) {
		builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
		routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic(transportmetadata.Topic)}}
		req := httptest.NewRequest(http.MethodPost, "/greet", strings.NewReader(transportmetadata.Body))
		req.Header.Set(wire.DefaultTopicKey, "not-the-topic") // a header is not the route
		httpbinding.Handler(builder, routes).ServeHTTP(httptest.NewRecorder(), req)
		if got := recorder.Only(t).Topic; got != transportmetadata.Topic {
			t.Errorf("topic = %q, want %q (from the route)", got, transportmetadata.Topic)
		}
	})

	// The listed divergence, held to what this port actually does so the claim cannot drift
	// silently either way: the Kafka topic routes, and a `topic` header stays a header.
	t.Run("kafka/awskafka.Handler", func(t *testing.T) {
		builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
		headerValue := make([]int, 0, len("not-the-topic"))
		for _, b := range []byte("not-the-topic") {
			headerValue = append(headerValue, int(b))
		}
		event := map[string]any{
			"eventSource": "aws:kafka",
			"records": map[string]any{
				transportmetadata.Topic + "-0": []map[string]any{{
					"topic":     transportmetadata.Topic,
					"partition": 0,
					"offset":    1,
					"value":     base64.StdEncoding.EncodeToString([]byte(transportmetadata.Body)),
					"headers":   []map[string][]int{{wire.DefaultTopicKey: headerValue}},
				}},
			},
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := awskafka.Handler(builder)(context.Background(), raw); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		got := recorder.Only(t)
		if got.Topic != transportmetadata.Topic {
			t.Errorf("topic = %q, want the Kafka topic %q", got.Topic, transportmetadata.Topic)
		}
		if got.Headers[wire.DefaultTopicKey] != "not-the-topic" {
			t.Errorf("headers = %v, want the `topic` header passed through as an ordinary header", got.Headers)
		}
	})
}
