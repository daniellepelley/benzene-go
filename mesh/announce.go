package mesh

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/healthcheck"
)

// Registration returns the register body for doc (mesh.md §2): the Contract Document the service
// serves on benzene:spec, with the deployment-side fields completed - Placement is detected
// (DetectPlacement) when doc carries none. Runtime and Degraded are contract.Derive's, and Profile is
// whatever the service's wiring put there (cloudservice.New sets it). None of them is in either hash,
// so the registrationHash the body carries is the served document's.
func Registration(doc contract.Document) contract.Document {
	if doc.Placement == nil {
		placement := DetectPlacement()
		doc.Placement = &placement
	}
	return doc
}

// NewHeartbeat builds one heartbeat (mesh.md §5) for an instance of the service doc describes:
// its identity and registrationHash from the registered document, the instance id, the time, and
// health - the aggregate health response, as healthcheck.Run returns it.
func NewHeartbeat(doc contract.Document, instanceID string, health healthcheck.Response) Heartbeat {
	if health.HealthChecks == nil {
		health.HealthChecks = map[string]healthcheck.CheckResult{}
	}
	return Heartbeat{
		Service:          doc.Service,
		ServiceVersion:   doc.ServiceVersion,
		InstanceID:       instanceID,
		RegistrationHash: doc.RegistrationHash,
		SentAt:           time.Now().UTC(),
		Health:           health,
	}
}

// AnnouncerOptions tunes an Announcer. Zero values mean the defaults.
type AnnouncerOptions struct {
	// InstanceID identifies this instance on heartbeats. Default: the host name.
	InstanceID string
	// Checks are run for every heartbeat (healthcheck.Run) - usually the same checks the service's
	// healthcheck.Middleware serves. None means a healthy report with no checks.
	Checks []healthcheck.Check
	// HeartbeatInterval is how often Run heartbeats once registered. Default 10s.
	HeartbeatInterval time.Duration
	// RetryInterval is the wait between Run's registration attempts. Default 2s.
	RetryInterval time.Duration
	// RegisterAttempts bounds Run's registration attempts; Run heartbeats whether or not one
	// landed. Default 30.
	RegisterAttempts int
	// OnSendFailure, when set, is told about every registration or heartbeat the collector did not
	// accept - the topic and the Sender's result - for logging. It must not block.
	OnSendFailure func(topic string, result benzene.Result[json.RawMessage])
}

// Announcer is the service side of registration and heartbeats (mesh.md §2, §5 - part of the
// meshed-service claim's M1, §7.1): it registers the service's Contract Document with a collector
// over any Sender and heartbeats the registered document's registrationHash with the service's
// aggregate health.
//
// Both feeds are best-effort. A failed registration or heartbeat is reported by the bool the method
// returns and otherwise dropped - an unreachable collector reduces the mesh, never the service - and
// a nil Sender yields a nil Announcer whose methods are no-ops.
type Announcer struct {
	sender  Sender
	doc     contract.Document
	body    []byte
	options AnnouncerOptions

	mu         sync.Mutex
	registered bool
}

// NewAnnouncer returns an Announcer that registers Registration(doc) through sender. doc is the
// document the service serves on benzene:spec (contract.Derive's result, or cloudservice.Service's
// Contract): one derivation serves both. A nil sender returns a nil Announcer.
func NewAnnouncer(sender Sender, doc contract.Document, options AnnouncerOptions) *Announcer {
	if sender == nil {
		return nil
	}
	if options.InstanceID == "" {
		options.InstanceID, _ = os.Hostname()
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = 10 * time.Second
	}
	if options.RetryInterval <= 0 {
		options.RetryInterval = 2 * time.Second
	}
	if options.RegisterAttempts <= 0 {
		options.RegisterAttempts = 30
	}
	doc = Registration(doc)
	// A derived document holds only strings, booleans, maps and slices of them: it always marshals.
	body, _ := json.Marshal(doc)
	return &Announcer{sender: sender, doc: doc, body: body, options: options}
}

// Registration returns the document the Announcer registers.
func (a *Announcer) Registration() contract.Document {
	if a == nil {
		return contract.Document{}
	}
	return a.doc
}

// Register sends the registration once and reports whether the collector accepted it.
func (a *Announcer) Register(ctx context.Context) bool {
	if a == nil {
		return false
	}
	ok := a.send(ctx, TopicRegister, a.body)
	if ok {
		a.mu.Lock()
		a.registered = true
		a.mu.Unlock()
	}
	return ok
}

// Heartbeat runs the configured checks and sends one heartbeat, reporting whether the collector
// accepted it.
func (a *Announcer) Heartbeat(ctx context.Context) bool {
	if a == nil {
		return false
	}
	hb := NewHeartbeat(a.doc, a.options.InstanceID, healthcheck.Run(ctx, a.options.Checks))
	// A Heartbeat holds strings, a time and a health response whose Data the checks supply; a
	// value encoding/json cannot write is the check's bug, and drops this heartbeat only.
	body, err := json.Marshal(hb)
	if err != nil {
		return false
	}
	return a.send(ctx, TopicHeartbeat, body)
}

// send delivers one message, reporting a failure to OnSendFailure.
func (a *Announcer) send(ctx context.Context, topic string, body []byte) bool {
	result := a.sender.Send(ctx, benzene.NewTopic(topic), map[string]string{}, body)
	if !result.IsSuccessful() && a.options.OnSendFailure != nil {
		a.options.OnSendFailure(topic, result)
	}
	return result.IsSuccessful()
}

// Run registers - retrying RegisterAttempts times, RetryInterval apart, while the collector comes
// up - then heartbeats every HeartbeatInterval until ctx is done. A registration that never landed is
// retried before each heartbeat, so a collector that comes up late (or restarts and forgets the
// fleet) still learns the contract. Run it in its own goroutine.
func (a *Announcer) Run(ctx context.Context) {
	if a == nil {
		return
	}
	for attempt := 1; !a.Register(ctx) && attempt < a.options.RegisterAttempts; attempt++ {
		if !sleep(ctx, a.options.RetryInterval) {
			return
		}
	}
	ticker := time.NewTicker(a.options.HeartbeatInterval)
	defer ticker.Stop()
	for {
		a.mu.Lock()
		registered := a.registered
		a.mu.Unlock()
		if !registered {
			a.Register(ctx)
		}
		a.Heartbeat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// sleep waits d or until ctx is done, reporting whether the full wait elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
