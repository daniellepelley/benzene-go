package domain

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
)

// declaredSends runs the real RegisterOutbound switch for service and returns the topic IDs it
// declared, in OutboundRegistry.Topics()' own sorted order.
func declaredSends(t *testing.T, service string) []string {
	t.Helper()
	outbound := mesh.NewOutboundRegistry()
	if err := RegisterOutbound(outbound, service); err != nil {
		t.Fatalf("RegisterOutbound(%q) error = %v", service, err)
	}
	ids := []string{}
	for _, topic := range outbound.Topics() {
		ids = append(ids, topic.ID)
	}
	return ids
}

func TestRegisterOutbound_DeclaresWhatEachServiceSends(t *testing.T) {
	// Sorted by topic ID, matching OutboundRegistry.Topics().
	for _, tc := range []struct {
		service string
		want    []string
	}{
		{ServiceOrders, []string{TopicOrderPlaced, TopicPaymentTake}},
		{ServicePayments, []string{TopicPaymentCaptured, TopicShipmentBook}},
		{ServiceShipping, []string{TopicShipmentDispatched}},
		{ServiceInventory, []string{}},
		{ServiceNotifications, []string{}},
		{ServiceAnalytics, []string{}},
	} {
		t.Run(tc.service, func(t *testing.T) {
			if got := declaredSends(t, tc.service); !slices.Equal(got, tc.want) {
				t.Errorf("declared sends = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRegisterOutbound_MatchesTheTopicsTheHandlersActuallySend is the test that keeps the
// declaration honest: it drives every handler with recording senders and compares the topics
// actually published against the topics RegisterOutbound declares. A hop added to a handler and
// left undeclared (or a declaration for a hop that was removed) fails here - which is the whole
// point, since nothing derives Consumes from the code itself (mesh.OutboundRegistry forbids
// exactly that kind of inference).
func TestRegisterOutbound_MatchesTheTopicsTheHandlersActuallySend(t *testing.T) {
	ctx := context.Background()

	t.Run(ServiceOrders, func(t *testing.T) {
		payments, orderPlaced := &recordingSender{}, &recordingSender{}
		CreateOrderHandler(payments, orderPlaced)(ctx, CreateOrderRequest{CustomerID: "c", SKU: "s", Quantity: 1})
		assertSentMatchesDeclared(t, ServiceOrders, payments, orderPlaced)
	})

	t.Run(ServicePayments, func(t *testing.T) {
		shipping, paymentCaptured := &recordingSender{}, &recordingSender{}
		TakePaymentHandler(shipping, paymentCaptured)(ctx, TakePaymentRequest{OrderID: "o", Amount: 10})
		assertSentMatchesDeclared(t, ServicePayments, shipping, paymentCaptured)
	})

	t.Run(ServiceShipping, func(t *testing.T) {
		shipmentDispatched := &recordingSender{}
		BookShipmentHandler(shipmentDispatched)(ctx, BookShipmentRequest{OrderID: "o", Carrier: "c"})
		assertSentMatchesDeclared(t, ServiceShipping, shipmentDispatched)
	})

	// The three pure event consumers hold no senders at all - AckHandler cannot publish - so
	// "declares nothing" is the only declaration that can match.
	for _, service := range []string{ServiceInventory, ServiceNotifications, ServiceAnalytics} {
		t.Run(service, func(t *testing.T) {
			assertSentMatchesDeclared(t, service)
		})
	}
}

// assertSentMatchesDeclared compares the topics senders actually recorded against what
// RegisterOutbound declares for service.
func assertSentMatchesDeclared(t *testing.T, service string, senders ...*recordingSender) {
	t.Helper()
	sent := []string{}
	for _, sender := range senders {
		for _, call := range sender.calls {
			if !slices.Contains(sent, call.topic.ID) {
				sent = append(sent, call.topic.ID)
			}
		}
	}
	slices.Sort(sent)

	if declared := declaredSends(t, service); !slices.Equal(sent, declared) {
		t.Errorf("%s sent %v but declares %v - the declaration and the send call sites disagree", service, sent, declared)
	}
}

func TestRegisterOutbound_UnknownServiceIsAnError(t *testing.T) {
	if err := RegisterOutbound(mesh.NewOutboundRegistry(), "not-a-service"); err == nil {
		t.Error("RegisterOutbound() error = nil, want an error for an unrecognised service")
	}
}

// TestRegisterOutbound_RejectsADuplicateDeclaration checks the startup error every multi-topic
// service surfaces rather than swallows: a second declaration of the same topic is a wiring bug,
// and RegisterOutbound propagates mesh.RegisterOutbound's error from whichever hop hit it first
// instead of continuing with a half-built registry.
func TestRegisterOutbound_RejectsADuplicateDeclaration(t *testing.T) {
	for _, service := range []string{ServiceOrders, ServicePayments, ServiceShipping} {
		t.Run(service, func(t *testing.T) {
			outbound := mesh.NewOutboundRegistry()
			if err := RegisterOutbound(outbound, service); err != nil {
				t.Fatalf("first RegisterOutbound() error = %v", err)
			}
			if err := RegisterOutbound(outbound, service); err == nil {
				t.Error("second RegisterOutbound() on the same registry = nil, want a duplicate-topic error")
			}
		})
	}
}

// -- the estate's declared graph ------------------------------------------------------------

// estateInbound is what each cmd/<service>/main.go registers as its handlers - the RECEIVE half
// of every service's contract, listed here so this package can assemble all six descriptors at
// once (each main's own test asserts that its real App descriptor carries these same topics, and
// the Consumes half declaredSends produces below). The values are the topics themselves; the
// handler each one is registered with is registerInbound's business.
var estateInbound = map[string][]string{
	ServiceOrders:        {TopicOrderCreate},
	ServicePayments:      {TopicPaymentTake},
	ServiceShipping:      {TopicShipmentBook},
	ServiceInventory:     {TopicOrderPlaced, TopicShipmentDispatched},
	ServiceNotifications: {TopicOrderPlaced, TopicPaymentCaptured, TopicShipmentDispatched},
	ServiceAnalytics:     {TopicPaymentCaptured, TopicShipmentDispatched},
}

// registerInbound registers the same handler cmd/<service>/main.go registers for topic. The
// senders are nil here: this test asserts the declared graph, which by design is knowable with no
// traffic and no transports wired at all (mesh.md §4).
func registerInbound(registry *benzene.Registry, topic string) error {
	switch topic {
	case TopicOrderCreate:
		return benzene.Register(registry, benzene.NewTopic(topic), CreateOrderHandler(nil, nil))
	case TopicPaymentTake:
		return benzene.Register(registry, benzene.NewTopic(topic), TakePaymentHandler(nil, nil))
	case TopicShipmentBook:
		return benzene.Register(registry, benzene.NewTopic(topic), BookShipmentHandler(nil))
	case TopicOrderPlaced:
		return benzene.Register(registry, benzene.NewTopic(topic), AckHandler[OrderPlaced]())
	case TopicPaymentCaptured:
		return benzene.Register(registry, benzene.NewTopic(topic), AckHandler[PaymentTaken]())
	default:
		return benzene.Register(registry, benzene.NewTopic(topic), AckHandler[ShipmentBooked]())
	}
}

// describeService builds one service's real mesh.Descriptor: handlers from estateInbound, and the
// outbound declaration from the production RegisterOutbound switch.
func describeService(t *testing.T, service string) mesh.Descriptor {
	t.Helper()
	registry := benzene.NewRegistry()
	for _, topic := range estateInbound[service] {
		if err := registerInbound(registry, topic); err != nil {
			t.Fatalf("register %s on %s: %v", topic, service, err)
		}
	}
	outbound := mesh.NewOutboundRegistry()
	if err := RegisterOutbound(outbound, service); err != nil {
		t.Fatalf("RegisterOutbound(%q) error = %v", service, err)
	}
	return mesh.Describe(registry, outbound, mesh.ServiceInfo{
		Service: service, ServiceVersion: "1.0.0", InstanceID: service, Binding: "azure-functions",
	})
}

// TestEstate_DeclaredProducerConsumerGraph is the end-to-end proof the whole outbound declaration
// exists for: register all six services' descriptors with a real meshd.Collector - no traffic, no
// traces, no transports - and read the topic catalog back. Every edge below comes from a declared
// descriptor and nothing else (mesh.md §4).
//
// Note the direction meshd's vocabulary uses, which is what these field names mean: a topic's
// PROVIDERS are the services that registered a handler for it (they answer it), and its CONSUMERS
// are the services that declared they send it (they call it) - store.go's register() writes
// providers from Descriptor.Topics and consumers from Descriptor.Consumes. So orders, which sends
// payment:take, appears on payment:take's consumers, and payments, which handles it, on its
// providers.
func TestEstate_DeclaredProducerConsumerGraph(t *testing.T) {
	ctx := context.Background()
	collector := meshd.New(meshd.Options{})

	for _, service := range []string{
		ServiceOrders, ServicePayments, ServiceShipping,
		ServiceInventory, ServiceNotifications, ServiceAnalytics,
	} {
		body, err := json.Marshal(describeService(t, service))
		if err != nil {
			t.Fatalf("marshal %s descriptor: %v", service, err)
		}
		if body, ok := dispatch(ctx, collector, mesh.TopicRegister, string(body)); !ok {
			t.Fatalf("register %s with the collector failed: %s", service, body)
		}
	}

	for _, tc := range []struct {
		topic     string
		providers []string // registered a handler for it
		consumers []string // declared they send it
	}{
		// The command chain: each hop's sender shows up as the declared consumer.
		{TopicPaymentTake, []string{ServicePayments}, []string{ServiceOrders}},
		{TopicShipmentBook, []string{ServiceShipping}, []string{ServicePayments}},
		// Event Hub fan-out: one declared sender, two handlers.
		{TopicOrderPlaced, []string{ServiceInventory, ServiceNotifications}, []string{ServiceOrders}},
		// Event Grid integration events.
		{TopicPaymentCaptured, []string{ServiceAnalytics, ServiceNotifications}, []string{ServicePayments}},
		{TopicShipmentDispatched, []string{ServiceAnalytics, ServiceInventory, ServiceNotifications}, []string{ServiceShipping}},
		// orders' own HTTP entry point: handled by orders, sent by nobody in the estate.
		{TopicOrderCreate, []string{ServiceOrders}, nil},
	} {
		t.Run(tc.topic, func(t *testing.T) {
			body, ok := dispatch(ctx, collector, mesh.TopicQueryTopic, `{"topic":"`+tc.topic+`"}`)
			if !ok {
				t.Fatalf("topic query failed: %s", body)
			}
			var summary meshd.TopicSummary
			if err := json.Unmarshal([]byte(body), &summary); err != nil {
				t.Fatalf("unmarshal topic summary: %v; body = %s", err, body)
			}
			// meshd sorts both lists, so a plain comparison is stable.
			if !slices.Equal(summary.Providers, tc.providers) {
				t.Errorf("%s providers = %v, want %v", tc.topic, summary.Providers, tc.providers)
			}
			if !slices.Equal(summary.Consumers, tc.consumers) {
				t.Errorf("%s consumers = %v, want %v", tc.topic, summary.Consumers, tc.consumers)
			}
		})
	}
}

// TestEstate_NoServiceReportsAMissingOutboundFeed pins the regression this declaration fixes: a
// service built without an OutboundRegistry still registers, but reports the outbound feed as
// degraded and lands on the catalog with no consumer edges at all - a silently half-drawn graph.
// Every service in this estate must instead announce both feeds intact.
func TestEstate_NoServiceReportsAMissingOutboundFeed(t *testing.T) {
	for service := range estateInbound {
		if degraded := describeService(t, service).Degraded; len(degraded) != 0 {
			t.Errorf("%s Degraded = %v, want none (both the registry and outbound feeds are wired)", service, degraded)
		}
	}
}

// dispatch sends one topic straight through the collector's own pipeline, returning its response
// body - no HTTP hop needed, this test IS in-process with it (the same
// envelope.DispatchTopicResult call meshapp's own collector tests use).
func dispatch(ctx context.Context, collector *meshd.Collector, topic, body string) (string, bool) {
	builder := collector.Builder()
	result, ok := envelope.DispatchTopicResult(ctx, builder.Pipeline, builder.Container, benzene.NewTopic(topic), nil, body)
	return result.Body, ok
}
