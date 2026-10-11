package mesh

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/healthcheck"
)

// collectorSender stands in for a collector: it records every message by topic and answers each
// with ok, or with service-unavailable while failing is set.
type collectorSender struct {
	mu       sync.Mutex
	messages map[string][][]byte
	failing  int // fail this many sends before succeeding; -1 fails forever
}

func (c *collectorSender) Send(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.messages == nil {
		c.messages = map[string][][]byte{}
	}
	c.messages[topic.ID] = append(c.messages[topic.ID], message)
	if c.failing != 0 {
		if c.failing > 0 {
			c.failing--
		}
		return benzene.ServiceUnavailable[json.RawMessage]("collector down")
	}
	return benzene.Ok(json.RawMessage(`{"accepted":1}`))
}

func (c *collectorSender) count(topic string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages[topic])
}

func (c *collectorSender) last(t *testing.T, topic string) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	sent := c.messages[topic]
	if len(sent) == 0 {
		t.Fatalf("nothing sent on %s", topic)
	}
	var body map[string]any
	if err := json.Unmarshal(sent[len(sent)-1], &body); err != nil {
		t.Fatalf("%s body is not JSON: %v", topic, err)
	}
	return body
}

func derivedDocument(t *testing.T, outbound *contract.OutboundRegistry, info contract.Info) contract.Document {
	t.Helper()
	doc, err := contract.Derive(newTestRegistry(t, benzene.NewTopic("order:create")), outbound, info)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return doc
}

func TestRegistration_DetectsPlacementOnlyWhenTheDocumentHasNone(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "orders")
	t.Setenv("AWS_REGION", "eu-west-1")

	detected := Registration(derivedDocument(t, contract.NewOutboundRegistry(), contract.Info{Service: "orders"}))
	if detected.Placement == nil || *detected.Placement != (Placement{Cloud: "aws", Region: "eu-west-1"}) {
		t.Errorf("Placement = %+v, want the detected aws/eu-west-1", detected.Placement)
	}

	configured := Registration(derivedDocument(t, contract.NewOutboundRegistry(), contract.Info{Service: "orders", Placement: &Placement{Cloud: "on-prem"}}))
	if *configured.Placement != (Placement{Cloud: "on-prem"}) {
		t.Errorf("Placement = %+v, want the configured override", configured.Placement)
	}
}

func TestNewHeartbeat_CarriesTheRegisteredIdentityAndHash(t *testing.T) {
	doc := derivedDocument(t, contract.NewOutboundRegistry(), contract.Info{Service: "orders", ServiceVersion: "1.4.2", VersionScheme: "semver"})
	hb := NewHeartbeat(doc, "orders-7f9c", healthcheck.Response{IsHealthy: true})

	if hb.Service != "orders" || hb.ServiceVersion != "1.4.2" || hb.InstanceID != "orders-7f9c" {
		t.Errorf("identity = %q/%q/%q", hb.Service, hb.ServiceVersion, hb.InstanceID)
	}
	if hb.RegistrationHash == "" || hb.RegistrationHash != doc.RegistrationHash {
		t.Errorf("RegistrationHash = %q, want the document's %q", hb.RegistrationHash, doc.RegistrationHash)
	}
	if hb.SentAt.IsZero() || hb.Health.HealthChecks == nil {
		t.Errorf("SentAt = %v, HealthChecks = %v: want a time and a non-nil map", hb.SentAt, hb.Health.HealthChecks)
	}
}

func TestAnnouncer_RegistersTheContractDocument(t *testing.T) {
	outbound := contract.NewOutboundRegistry()
	contract.MustRegisterEvent[echoResponse](outbound, benzene.NewTopic("order:placed"))
	doc := derivedDocument(t, outbound, contract.Info{
		Service: "orders", ServiceVersion: "1.4.2", VersionScheme: "semver",
		Placement: &Placement{Cloud: "self-hosted"},
		Profile:   &contract.Profile{Name: "cloud-service", Missing: []string{"R8"}},
	})
	sender := &collectorSender{}
	announcer := NewAnnouncer(sender, doc, AnnouncerOptions{InstanceID: "orders-1"})

	if !announcer.Register(context.Background()) {
		t.Fatal("Register() = false, want the collector's acceptance")
	}
	body := sender.last(t, TopicRegister)
	for key, want := range map[string]any{
		"service": "orders", "serviceVersion": "1.4.2", "versionScheme": "semver", "runtime": "go",
		"registrationHash": doc.RegistrationHash, "contractHash": doc.ContractHash,
	} {
		if body[key] != want {
			t.Errorf("register body %s = %v, want %v", key, body[key], want)
		}
	}
	if _, ok := body["instanceId"]; ok {
		t.Error("the register body carries an instanceId; only heartbeats and traces do")
	}
	if _, ok := body["degraded"]; ok {
		t.Error("a fully declared document carries degraded")
	}
	if events := body["events"].([]any); len(events) != 1 {
		t.Errorf("events = %v, want the one declared event", events)
	}
	if profile := body["profile"].(map[string]any); profile["name"] != "cloud-service" {
		t.Errorf("profile = %v", profile)
	}
	if got := announcer.Registration(); got.RegistrationHash != doc.RegistrationHash {
		t.Errorf("Registration().RegistrationHash = %q", got.RegistrationHash)
	}
}

func TestAnnouncer_RegisterBodyNamesTheMembersItCouldNotDetermine(t *testing.T) {
	sender := &collectorSender{}
	announcer := NewAnnouncer(sender, derivedDocument(t, nil, contract.Info{Service: "orders"}), AnnouncerOptions{})
	announcer.Register(context.Background())

	degraded, _ := sender.last(t, TopicRegister)["degraded"].([]any)
	if len(degraded) != 2 || degraded[0] != "events" || degraded[1] != "sends" {
		t.Errorf("degraded = %v, want [events sends]", degraded)
	}
}

func TestAnnouncer_HeartbeatRunsTheChecks(t *testing.T) {
	doc := derivedDocument(t, contract.NewOutboundRegistry(), contract.Info{Service: "orders", ServiceVersion: "2"})
	sender := &collectorSender{}
	announcer := NewAnnouncer(sender, doc, AnnouncerOptions{
		InstanceID: "orders-1",
		Checks: []healthcheck.Check{healthcheck.NamedCheck("db", func(context.Context) healthcheck.CheckResult {
			return healthcheck.CheckResult{Status: healthcheck.StatusFailed, Type: "postgres"}
		})},
	})

	if !announcer.Heartbeat(context.Background()) {
		t.Fatal("Heartbeat() = false, want the collector's acceptance")
	}
	body := sender.last(t, TopicHeartbeat)
	if body["service"] != "orders" || body["serviceVersion"] != "2" || body["instanceId"] != "orders-1" || body["registrationHash"] != doc.RegistrationHash {
		t.Errorf("heartbeat identity = %v", body)
	}
	health := body["health"].(map[string]any)
	if health["isHealthy"] != false || health["healthChecks"].(map[string]any)["db"] == nil {
		t.Errorf("health = %v, want the failed db check", health)
	}
}

func TestAnnouncer_UnmarshalableHealthDropsTheHeartbeat(t *testing.T) {
	sender := &collectorSender{}
	announcer := NewAnnouncer(sender, derivedDocument(t, nil, contract.Info{Service: "orders"}), AnnouncerOptions{
		Checks: []healthcheck.Check{healthcheck.NamedCheck("bad", func(context.Context) healthcheck.CheckResult {
			return healthcheck.CheckResult{Status: healthcheck.StatusOk, Data: map[string]any{"c": make(chan int)}}
		})},
	})
	if announcer.Heartbeat(context.Background()) {
		t.Error("Heartbeat() = true for a body encoding/json cannot write")
	}
	if sender.count(TopicHeartbeat) != 0 {
		t.Error("an unwritable heartbeat was sent")
	}
}

func TestAnnouncer_DefaultsTheInstanceIDToTheHostName(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no host name on this platform")
	}
	sender := &collectorSender{}
	NewAnnouncer(sender, derivedDocument(t, nil, contract.Info{Service: "orders"}), AnnouncerOptions{}).Heartbeat(context.Background())
	if got := sender.last(t, TopicHeartbeat)["instanceId"]; got != host {
		t.Errorf("instanceId = %v, want the host name %q", got, host)
	}
}

func TestAnnouncer_NilSenderIsANilNoOpAnnouncer(t *testing.T) {
	announcer := NewAnnouncer(nil, contract.Document{Service: "orders"}, AnnouncerOptions{})
	if announcer != nil {
		t.Fatal("NewAnnouncer(nil, ...) is not nil")
	}
	ctx := context.Background()
	if announcer.Register(ctx) || announcer.Heartbeat(ctx) {
		t.Error("a nil Announcer reported a send")
	}
	if announcer.Registration().Service != "" {
		t.Error("a nil Announcer has a registration")
	}
	announcer.Run(ctx) // returns at once
}

func TestAnnouncer_RunRetriesRegistrationThenHeartbeats(t *testing.T) {
	sender := &collectorSender{failing: 2}
	announcer := NewAnnouncer(sender, derivedDocument(t, nil, contract.Info{Service: "orders"}), AnnouncerOptions{
		RetryInterval: time.Millisecond, HeartbeatInterval: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { announcer.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for sender.count(TopicHeartbeat) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if got := sender.count(TopicRegister); got != 3 {
		t.Errorf("register sent %d times, want 3 (two failures, then one landing)", got)
	}
	if sender.count(TopicHeartbeat) < 2 {
		t.Error("Run did not keep heartbeating")
	}
}

func TestAnnouncer_RunKeepsRegisteringUntilTheCollectorLearnsTheContract(t *testing.T) {
	// Every send fails until the attempts run out; Run then re-registers before each heartbeat.
	sender := &collectorSender{failing: -1}
	announcer := NewAnnouncer(sender, derivedDocument(t, nil, contract.Info{Service: "orders"}), AnnouncerOptions{
		RetryInterval: time.Millisecond, HeartbeatInterval: time.Millisecond, RegisterAttempts: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { announcer.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for sender.count(TopicHeartbeat) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if got, heartbeats := sender.count(TopicRegister), sender.count(TopicHeartbeat); got < 2+heartbeats-1 {
		t.Errorf("register sent %d times across %d heartbeats, want a retry before each", got, heartbeats)
	}
}

func TestAnnouncer_ReportsSendFailures(t *testing.T) {
	var failures []string
	announcer := NewAnnouncer(&collectorSender{failing: -1}, derivedDocument(t, nil, contract.Info{Service: "orders"}), AnnouncerOptions{
		OnSendFailure: func(topic string, result benzene.Result[json.RawMessage]) {
			failures = append(failures, topic+" "+string(result.Status))
		},
	})
	announcer.Register(context.Background())
	announcer.Heartbeat(context.Background())
	if len(failures) != 2 || failures[0] != TopicRegister+" service-unavailable" || failures[1] != TopicHeartbeat+" service-unavailable" {
		t.Errorf("failures = %v, want the register and the heartbeat", failures)
	}
}

func TestAnnouncer_RunStopsWhenCancelledWhileRetrying(t *testing.T) {
	sender := &collectorSender{failing: -1}
	announcer := NewAnnouncer(sender, derivedDocument(t, nil, contract.Info{Service: "orders"}), AnnouncerOptions{RetryInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { announcer.Run(ctx); close(done) }()

	for sender.count(TopicRegister) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept waiting after its context was cancelled")
	}
	if sender.count(TopicHeartbeat) != 0 {
		t.Error("Run heartbeated after being cancelled mid-retry")
	}
}
