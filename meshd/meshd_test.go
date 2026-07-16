package meshd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/wire"
)

var testClock = time.Date(2026, 7, 16, 9, 14, 3, 0, time.UTC)

func newTestCollector(t *testing.T) *Collector {
	t.Helper()
	return New(Options{Now: func() time.Time { return testClock }})
}

// invoke sends one wire envelope through the collector's pipeline - the same path a real
// network caller takes - and unmarshals the response body into T.
func invoke[T any](t *testing.T, c *Collector, topic string, body any) (benzene.Status, T) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	builder := c.Builder()
	resp := envelope.Dispatch(context.Background(), builder.Pipeline, builder.Container, wire.Request{
		Topic:   topic,
		Headers: map[string]string{},
		Body:    string(payload),
	})

	var typed T
	if benzene.Status(resp.StatusCode).IsSuccess() {
		if err := json.Unmarshal([]byte(resp.Body), &typed); err != nil {
			t.Fatalf("unmarshal %q response %q: %v", topic, resp.Body, err)
		}
	}
	return benzene.Status(resp.StatusCode), typed
}

// testDescriptor builds a real derived descriptor; topics use "id" or "id@version".
func testDescriptor(service string, topics ...string) mesh.Descriptor {
	registry := benzene.NewRegistry()
	for _, spec := range topics {
		topic := benzene.NewTopic(spec)
		if id, version, versioned := strings.Cut(spec, "@"); versioned {
			topic = benzene.NewTopic(id).WithVersion(version)
		}
		if err := benzene.Register(registry, topic,
			benzene.Handler[struct{}, struct{}](func(context.Context, struct{}) benzene.Result[struct{}] {
				return benzene.Ok(struct{}{})
			})); err != nil {
			panic(err)
		}
	}
	return mesh.Describe(registry, mesh.ServiceInfo{
		Service:   service,
		Binding:   "http",
		Placement: mesh.Placement{Cloud: "aws", Region: "eu-west-1"},
	})
}

func testHeartbeat(service, instance string, healthy bool, hash string) mesh.Heartbeat {
	return mesh.Heartbeat{
		Service:        service,
		InstanceID:     instance,
		DescriptorHash: hash,
		SentAt:         testClock,
		Health:         healthcheck.Response{IsHealthy: healthy, HealthChecks: map[string]healthcheck.CheckResult{}},
	}
}

func event(traceID, spanID, parentSpanID, service, topic, status string, durationMs float64) mesh.TraceEvent {
	return mesh.TraceEvent{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Service:      service,
		Topic:        topic,
		Status:       status,
		DurationMs:   durationMs,
		StartedAt:    testClock,
	}
}

func TestCollector_RegisterAndFleet(t *testing.T) {
	c := newTestCollector(t)

	status, ack := invoke[Ack](t, c, mesh.TopicRegister, testDescriptor("orders", "order:create", "order:create@v2", "order:cancel"))
	if status != benzene.StatusOk || ack.Accepted != 1 {
		t.Fatalf("register = (%v, %+v), want (Ok, accepted 1)", status, ack)
	}

	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})

	if len(fleet.Services) != 1 {
		t.Fatalf("Services = %+v, want the registered service", fleet.Services)
	}
	svc := fleet.Services[0]
	if svc.Service != "orders" || svc.Topics != 3 || svc.Placement.Cloud != "aws" || svc.Binding != "http" {
		t.Errorf("service row = %+v, want descriptor-derived identity", svc)
	}
	if svc.Health != healthUnknown {
		t.Errorf("Health = %q, want %q with no heartbeats", svc.Health, healthUnknown)
	}
	if len(svc.MissingFeeds) != 2 || svc.MissingFeeds[0] != "health" || svc.MissingFeeds[1] != "traces" {
		t.Errorf("MissingFeeds = %v, want [health traces] - reduced is visible, not an error", svc.MissingFeeds)
	}
	if svc.LastSeen != testClock {
		t.Errorf("LastSeen = %v, want the ingest clock %v", svc.LastSeen, testClock)
	}
	// A registered topic with no traffic is a catalog entry with no stats, and the
	// catalog sorts by id then version.
	wantOrder := []struct{ id, version string }{{"order:cancel", ""}, {"order:create", ""}, {"order:create", "v2"}}
	if len(fleet.Topics) != len(wantOrder) {
		t.Fatalf("Topics = %+v, want all three registered topics", fleet.Topics)
	}
	for i, want := range wantOrder {
		if fleet.Topics[i].Topic != want.id || fleet.Topics[i].Version != want.version || fleet.Topics[i].Invocations != 0 {
			t.Errorf("Topics[%d] = %+v, want %v with zero stats", i, fleet.Topics[i], want)
		}
	}
	if len(fleet.Topics[0].Providers) != 1 || fleet.Topics[0].Providers[0] != "orders" {
		t.Errorf("Providers = %v, want [orders]", fleet.Topics[0].Providers)
	}
}

func TestCollector_ReregistrationReplacesProviderEdges(t *testing.T) {
	c := newTestCollector(t)

	invoke[Ack](t, c, mesh.TopicRegister, testDescriptor("orders", "order:create"))
	invoke[Ack](t, c, mesh.TopicRegister, testDescriptor("orders", "order:cancel"))

	status, topic := invoke[TopicSummary](t, c, mesh.TopicQueryTopic, TopicQuery{Topic: "order:create"})
	if status != benzene.StatusOk {
		t.Fatalf("topic query = %v, want Ok (the topic row survives with its stats)", status)
	}
	if len(topic.Providers) != 0 {
		t.Errorf("Providers = %v, want none after the redeploy dropped the topic", topic.Providers)
	}
}

func TestCollector_Validation(t *testing.T) {
	c := newTestCollector(t)

	tests := []struct {
		name  string
		topic string
		body  any
	}{
		{name: "register requires a service", topic: mesh.TopicRegister, body: mesh.Descriptor{}},
		{name: "heartbeat requires a service", topic: mesh.TopicHeartbeat, body: mesh.Heartbeat{}},
		{name: "service query requires a service", topic: mesh.TopicQueryService, body: ServiceQuery{}},
		{name: "topic query requires a topic", topic: mesh.TopicQueryTopic, body: TopicQuery{}},
		{name: "trace query requires a trace id", topic: mesh.TopicQueryTrace, body: TraceQuery{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, _ := invoke[struct{}](t, c, tt.topic, tt.body); status != benzene.StatusBadRequest {
				t.Errorf("status = %v, want BadRequest", status)
			}
		})
	}
}

func TestCollector_NotFound(t *testing.T) {
	c := newTestCollector(t)

	tests := []struct {
		name  string
		topic string
		body  any
	}{
		{name: "unknown service", topic: mesh.TopicQueryService, body: ServiceQuery{Service: "ghost"}},
		{name: "unknown topic", topic: mesh.TopicQueryTopic, body: TopicQuery{Topic: "no:such"}},
		{name: "unknown trace", topic: mesh.TopicQueryTrace, body: TraceQuery{TraceID: "feed0000feed0000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, _ := invoke[struct{}](t, c, tt.topic, tt.body); status != benzene.StatusNotFound {
				t.Errorf("status = %v, want NotFound", status)
			}
		})
	}
}

func TestCollector_HeartbeatsDriveHealth(t *testing.T) {
	c := newTestCollector(t)
	desc := testDescriptor("orders", "order:create")
	invoke[Ack](t, c, mesh.TopicRegister, desc)

	invoke[Ack](t, c, mesh.TopicHeartbeat, testHeartbeat("orders", "orders-1", true, desc.DescriptorHash))
	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	if fleet.Services[0].Health != healthHealthy || fleet.Services[0].Instances != 1 {
		t.Errorf("service = %+v, want healthy with 1 instance", fleet.Services[0])
	}

	invoke[Ack](t, c, mesh.TopicHeartbeat, testHeartbeat("orders", "orders-2", false, "sha256:different"))
	_, fleet = invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	if fleet.Services[0].Health != healthDegraded || fleet.Services[0].Instances != 2 {
		t.Errorf("service = %+v, want degraded once any instance is unhealthy", fleet.Services[0])
	}

	status, view := invoke[ServiceView](t, c, mesh.TopicQueryService, ServiceQuery{Service: "orders"})
	if status != benzene.StatusOk || view.Descriptor == nil || len(view.Instances) != 2 {
		t.Fatalf("service view = (%v, %+v), want Ok with descriptor and 2 instances", status, view)
	}
	if view.Instances[0].HashMatches == nil || !*view.Instances[0].HashMatches {
		t.Errorf("orders-1 HashMatches = %v, want true (heartbeat hash equals registered hash)", view.Instances[0].HashMatches)
	}
	if view.Instances[1].HashMatches == nil || *view.Instances[1].HashMatches {
		t.Errorf("orders-2 HashMatches = %v, want false (contract drift is visible)", view.Instances[1].HashMatches)
	}
}

func TestCollector_HashMatchNilWhenEitherSideLacksAHash(t *testing.T) {
	c := newTestCollector(t)
	invoke[Ack](t, c, mesh.TopicRegister, testDescriptor("orders", "order:create"))
	invoke[Ack](t, c, mesh.TopicHeartbeat, testHeartbeat("orders", "orders-1", true, ""))

	_, view := invoke[ServiceView](t, c, mesh.TopicQueryService, ServiceQuery{Service: "orders"})

	if view.Instances[0].HashMatches != nil {
		t.Errorf("HashMatches = %v, want nil when the heartbeat carried no hash", *view.Instances[0].HashMatches)
	}
}

func TestCollector_TracesDriveStatsAndConsumers(t *testing.T) {
	c := newTestCollector(t)
	invoke[Ack](t, c, mesh.TopicRegister, testDescriptor("greeter", "greet"))

	status, ack := invoke[Ack](t, c, mesh.TopicTraces, mesh.TraceBatch{Events: []mesh.TraceEvent{
		event("trace-1", "span-front", "", "frontdoor", "welcome", "Ok", 20),
		event("trace-1", "span-greet", "span-front", "greeter", "greet", "Ok", 10),
		event("trace-2", "span-fail", "", "greeter", "greet", "ServiceUnavailable", 30),
		event("trace-2", "span-self", "span-fail", "greeter", "greet", "Ok", 5), // same-service parent: no edge
	}})
	if status != benzene.StatusOk || ack.Accepted != 4 {
		t.Fatalf("traces = (%v, %+v), want (Ok, accepted 4)", status, ack)
	}

	topicStatus, greet := invoke[TopicSummary](t, c, mesh.TopicQueryTopic, TopicQuery{Topic: "greet"})
	if topicStatus != benzene.StatusOk {
		t.Fatalf("topic query = %v, want Ok", topicStatus)
	}
	if greet.Invocations != 3 || greet.Errors != 1 {
		t.Errorf("greet stats = %+v, want 3 invocations, 1 error", greet)
	}
	if greet.StatusCounts["Ok"] != 2 || greet.StatusCounts["ServiceUnavailable"] != 1 {
		t.Errorf("StatusCounts = %v, want Ok:2 ServiceUnavailable:1", greet.StatusCounts)
	}
	if greet.AvgDurationMs != 15 {
		t.Errorf("AvgDurationMs = %v, want 15", greet.AvgDurationMs)
	}
	if len(greet.Consumers) != 1 || greet.Consumers[0] != "frontdoor" {
		t.Errorf("Consumers = %v, want [frontdoor] derived from parentage, same-service edges excluded", greet.Consumers)
	}

	// frontdoor never registered: anonymous but live, visibly reduced.
	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	if len(fleet.Services) != 2 { // frontdoor and greeter, nothing else
		t.Fatalf("Services = %+v, want frontdoor and greeter", fleet.Services)
	}
	frontdoor := fleet.Services[0]
	if frontdoor.Service != "frontdoor" || frontdoor.Invocations != 1 {
		t.Errorf("frontdoor = %+v, want trace-derived row", frontdoor)
	}
	if len(frontdoor.MissingFeeds) != 2 || frontdoor.MissingFeeds[0] != "descriptor" || frontdoor.MissingFeeds[1] != "health" {
		t.Errorf("frontdoor MissingFeeds = %v, want [descriptor health]", frontdoor.MissingFeeds)
	}

	// Fleet flow summaries: trace-2 failed, trace-1 succeeded.
	if len(fleet.Traces) != 2 {
		t.Fatalf("Traces = %+v, want 2 flows", fleet.Traces)
	}
	for _, flow := range fleet.Traces {
		switch flow.TraceID {
		case "trace-1":
			if flow.Failed || flow.Events != 2 || len(flow.Services) != 2 {
				t.Errorf("trace-1 = %+v, want 2 events across frontdoor+greeter, not failed", flow)
			}
		case "trace-2":
			if !flow.Failed {
				t.Errorf("trace-2 = %+v, want failed (it contains a ServiceUnavailable)", flow)
			}
		default:
			t.Errorf("unexpected flow %+v", flow)
		}
	}
}

func TestCollector_AnonymousEventsCountTopicsButNoService(t *testing.T) {
	c := newTestCollector(t)

	invoke[Ack](t, c, mesh.TopicTraces, mesh.TraceBatch{Events: []mesh.TraceEvent{
		event("trace-1", "span-1", "", "", "greet", "Ok", 10),
	}})

	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	if len(fleet.Services) != 0 {
		t.Errorf("Services = %+v, want none for an identity-less event", fleet.Services)
	}
	if len(fleet.Topics) != 1 || fleet.Topics[0].Invocations != 1 {
		t.Errorf("Topics = %+v, want the topic counted anyway", fleet.Topics)
	}
}

func TestCollector_TraceQueryAndRingEviction(t *testing.T) {
	c := New(Options{MaxTraceEvents: 2, Now: func() time.Time { return testClock }})

	later := event("trace-1", "span-2", "", "greeter", "greet", "Ok", 5)
	later.StartedAt = testClock.Add(time.Second)
	invoke[Ack](t, c, mesh.TopicTraces, mesh.TraceBatch{Events: []mesh.TraceEvent{
		later,
		event("trace-1", "span-1", "", "frontdoor", "welcome", "Ok", 20),
	}})

	status, view := invoke[TraceView](t, c, mesh.TopicQueryTrace, TraceQuery{TraceID: "trace-1"})
	if status != benzene.StatusOk || len(view.Events) != 2 {
		t.Fatalf("trace query = (%v, %+v), want both events", status, view)
	}
	if view.Events[0].SpanID != "span-1" || view.Events[1].SpanID != "span-2" {
		t.Errorf("events = %+v, want sorted by start time", view.Events)
	}

	// The fleet flow summary must anchor on the earliest event even though a later one
	// was ingested first, and span the flow end to end.
	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	if len(fleet.Traces) != 1 || !fleet.Traces[0].StartedAt.Equal(testClock) {
		t.Fatalf("Traces = %+v, want one flow starting at the earliest event", fleet.Traces)
	}
	if got := fleet.Traces[0].DurationMs; got != 1005 { // T+1s+5ms end vs T start
		t.Errorf("DurationMs = %v, want 1005", got)
	}

	// Two more events overwrite the full ring; trace-1 ages out of the window.
	invoke[Ack](t, c, mesh.TopicTraces, mesh.TraceBatch{Events: []mesh.TraceEvent{
		event("trace-2", "span-3", "", "greeter", "greet", "Ok", 5),
		event("trace-2", "span-4", "", "greeter", "greet", "Ok", 5),
	}})
	if status, _ := invoke[TraceView](t, c, mesh.TopicQueryTrace, TraceQuery{TraceID: "trace-1"}); status != benzene.StatusNotFound {
		t.Errorf("evicted trace query = %v, want NotFound (the window is bounded by design)", status)
	}
	// Cumulative stats survive eviction.
	_, greet := invoke[TopicSummary](t, c, mesh.TopicQueryTopic, TopicQuery{Topic: "greet"})
	if greet.Invocations != 3 {
		t.Errorf("greet invocations = %d, want 3 - stats outlive the ring window", greet.Invocations)
	}
}

func TestCollector_FleetTraceListIsBounded(t *testing.T) {
	c := newTestCollector(t)

	events := make([]mesh.TraceEvent, 0, maxFleetTraces+5)
	for i := 0; i < maxFleetTraces+5; i++ {
		e := event("trace-"+string(rune('a'+i)), "span-"+string(rune('a'+i)), "", "svc", "topic", "Ok", 1)
		e.StartedAt = testClock.Add(time.Duration(i) * time.Second)
		events = append(events, e)
	}
	invoke[Ack](t, c, mesh.TopicTraces, mesh.TraceBatch{Events: events})

	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	if len(fleet.Traces) != maxFleetTraces {
		t.Fatalf("Traces = %d flows, want capped at %d", len(fleet.Traces), maxFleetTraces)
	}
	if fleet.Traces[0].StartedAt.Before(fleet.Traces[1].StartedAt) {
		t.Errorf("flows not newest-first: %v then %v", fleet.Traces[0].StartedAt, fleet.Traces[1].StartedAt)
	}
}

func TestCollector_ServesItsOwnDescriptor(t *testing.T) {
	c := newTestCollector(t)

	status, desc := invoke[mesh.Descriptor](t, c, mesh.TopicID, struct{}{})

	if status != benzene.StatusOk || desc.Service != "meshd" {
		t.Fatalf("mesh topic = (%v, %+v), want the collector's own descriptor", status, desc)
	}
	topics := map[string]bool{}
	for _, topic := range desc.Topics {
		topics[topic.ID] = true
	}
	for _, want := range []string{mesh.TopicRegister, mesh.TopicHeartbeat, mesh.TopicTraces, mesh.TopicQueryFleet, mesh.TopicQueryService, mesh.TopicQueryTopic, mesh.TopicQueryTrace} {
		if !topics[want] {
			t.Errorf("collector descriptor is missing its own topic %q: %v", want, desc.Topics)
		}
	}
}

func TestCollector_DefaultsApplied(t *testing.T) {
	c := New(Options{})

	if c.store.capacity != 4096 {
		t.Errorf("capacity = %d, want the 4096 default", c.store.capacity)
	}
	if c.store.now == nil {
		t.Error("now = nil, want time.Now default")
	}
}

func TestMustRegister(t *testing.T) {
	t.Run("nil error passes", func(t *testing.T) {
		mustRegister(nil)
	})
	t.Run("registration bug panics at startup", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("mustRegister(err) did not panic")
			}
		}()
		mustRegister(context.DeadlineExceeded)
	})
}

func TestAck_WireFieldNamesAreCamelCase(t *testing.T) {
	data, err := json.Marshal(Ack{Accepted: 3})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(data) != `{"accepted":3}` {
		t.Errorf("marshaled ack = %s, want {\"accepted\":3}", data)
	}
}
