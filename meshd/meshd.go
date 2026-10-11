// Package meshd implements the Benzene Mesh collector (originally Phases 3-4 of this repo's own
// work/archive/mesh.md, now the main repo's docs/specification/mesh.md §§4-6). It is itself an
// ordinary Benzene service - its benzene:mesh:* topics live in a Registry, are served through a
// Pipeline, and it serves its own Contract Document on the reserved benzene:spec topic - so deploying it
// anywhere this module's bindings reach (Lambda, Functions, Cloud Run, plain HTTP) is the same
// exercise as deploying any other service.
//
// A benzene:mesh:register body is the service's Contract Document (mesh.md §2). The
// producer/consumer graph (store.go's register) is built solely from the latest registered document -
// requests for consumers, events ∪ sends for providers, benzene: entries ignored - present in full
// for a service with zero traffic, and replaced wholesale on re-registration. A body carrying only
// service is accepted: an absent member, or one the document names in degraded, is unknown, and
// declares no edge. Trace parentage (store.go's addEvents) never touches
// that graph; it feeds invocation stats plus two additive, observed-only signals (mesh.md §4.2):
// per-declared-edge last-observed-at, and contract-drift issues when a *registered* service's
// traffic names a topic it didn't declare.
//
// The collector accepts partial fleets, mirroring the mesh package's degradation rule:
// traces from a service that never registered render it anonymous-but-live (its row shows
// the missing registration - the "descriptor" feed, in the read models' vocabulary - and it is
// never eligible for a drift issue: it has no declared contract to diverge from); a registered service with no traffic is a catalog entry
// with no stats; a service without heartbeats has unknown health. Reduced feeds reduce
// the view - they never make ingestion or queries fail.
//
// The store is in-memory (cumulative stats plus a bounded ring of recent trace events),
// which is the MVP tier of mesh.md §8/§9 - a restart forgets history and re-learns the
// fleet from the next heartbeats and traces.
package meshd

import (
	"context"
	"encoding/json"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/mesh"
)

// Options tunes a Collector. Zero values mean the defaults.
type Options struct {
	// MaxTraceEvents bounds the in-memory trace-event ring the flow explorer and
	// consumer-edge derivation read from. Cumulative stats are not bounded by it.
	// Default 4096.
	MaxTraceEvents int
	// Now is a test hook for the clock. Default time.Now.
	Now func() time.Time
}

// Ack acknowledges an ingest message (register/heartbeat/traces): how many items were
// accepted.
type Ack struct {
	Accepted int `json:"accepted"`
}

// FleetView is the benzene:mesh:query:fleet response: the whole known fleet in one shape - what
// the Mesh View renders (mesh.md §6.1/§6.2/§6.4).
type FleetView struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Services    []ServiceSummary `json:"services"`
	Topics      []TopicSummary   `json:"topics"`
	Traces      []TraceSummary   `json:"traces"`
	Issues      []IssueView      `json:"issues"`
}

// IssueView is one merged failure signature on the fleet view (mesh.md §4.1). Unlike the wire
// Issue's delta Count, this Count is the cumulative total the collector has merged.
type IssueView struct {
	Fingerprint      string    `json:"fingerprint"`
	Classification   string    `json:"classification"`
	Service          string    `json:"service"`
	Topic            string    `json:"topic"`
	Version          string    `json:"version,omitempty"`
	Transport        string    `json:"transport,omitempty"`
	Status           string    `json:"status"`
	ExceptionType    string    `json:"exceptionType,omitempty"`
	Count            int64     `json:"count"`
	FirstSeen        time.Time `json:"firstSeen"`
	LastSeen         time.Time `json:"lastSeen"`
	ExemplarTraceIds []string  `json:"exemplarTraceIds,omitempty"`
	ResolutionHint   string    `json:"resolutionHint,omitempty"`
}

// Health classification of a service, from its instances' latest heartbeats.
const (
	healthHealthy  = "healthy"
	healthDegraded = "degraded"
	healthUnknown  = "unknown"
)

// ServiceSummary is one service's fleet row, read from its registered Contract Document: Topics
// counts its requests (the topics it serves) and Produces its events and sends (the topics it
// provides), benzene: entries excluded. MissingFeeds names the feeds the collector has not received
// for it ("descriptor" - the registration -, "health", "traces", "issues") - a reduced service is
// shown as reduced, never mistaken for an empty or dead one.
type ServiceSummary struct {
	Service        string              `json:"service"`
	ServiceVersion string              `json:"serviceVersion,omitempty"`
	Runtime        string              `json:"runtime,omitempty"`
	Transports     []string            `json:"transports,omitempty"`
	Placement      *contract.Placement `json:"placement,omitempty"`
	Topics         int                 `json:"topics"`
	Produces       int                 `json:"produces"`
	Instances      int                 `json:"instances"`
	Health         string              `json:"health"`
	LastSeen       time.Time           `json:"lastSeen"`
	Invocations    int64               `json:"invocations"`
	Errors         int64               `json:"errors"`
	MissingFeeds   []string            `json:"missingFeeds,omitempty"`
}

// TopicSummary is one topic's catalog row. Providers/Consumers are the declared
// producer/consumer graph (mesh.md §4): built from the latest registered documents' events ∪ sends
// and requests alone, present even for a topic with zero traffic - never derived from trace
// parentage. ProviderActivity/ConsumerActivity are the additive, observed-only signal of mesh.md
// §4.2 layered on top: per declared edge, the last time a trace actually observed it (absent
// means never observed in the retention window - a decommission candidate, not a fact).
type TopicSummary struct {
	Topic            string                  `json:"topic"`
	Version          string                  `json:"version,omitempty"`
	Providers        []string                `json:"providers,omitempty"`
	Consumers        []string                `json:"consumers,omitempty"`
	ProviderActivity map[string]EdgeActivity `json:"providerActivity,omitempty"`
	ConsumerActivity map[string]EdgeActivity `json:"consumerActivity,omitempty"`
	Invocations      int64                   `json:"invocations"`
	Errors           int64                   `json:"errors"`
	AvgDurationMs    float64                 `json:"avgDurationMs"`
	StatusCounts     map[string]int64        `json:"statusCounts,omitempty"`
	LastSeen         time.Time               `json:"lastSeen"`
}

// EdgeActivity is one declared edge's observed-activity record (mesh.md §4.2 "Unobserved"): the
// last time a trace showed it being exercised, or the zero value when it never has been within
// the collector's retention window - reported explicitly per edge rather than collapsed to a
// boolean, so a reader can judge staleness for itself. On the wire a never-observed edge is an
// entry with lastObservedAt absent ({}), never the zero time: omitzero, because omitempty does not
// omit a struct such as time.Time.
type EdgeActivity struct {
	LastObservedAt time.Time `json:"lastObservedAt,omitzero"`
}

// TraceSummary is one recent flow on the fleet view.
type TraceSummary struct {
	TraceID    string    `json:"traceId"`
	Events     int       `json:"events"`
	Services   []string  `json:"services,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs float64   `json:"durationMs"`
	Failed     bool      `json:"failed"`
}

// ServiceView is the benzene:mesh:query:service response. Descriptor is the registered Contract
// Document exactly as the service sent it (the key keeps the read models' "descriptor" feed name).
type ServiceView struct {
	ServiceSummary
	Descriptor json.RawMessage `json:"descriptor,omitempty"`
	Instances  []InstanceView  `json:"instances,omitempty"`
}

// InstanceView is one instance's latest heartbeat state. HashMatches compares the heartbeat's
// registrationHash with the registered document's (mesh.md §5; this collector keys by service, so
// with the service's one registration) - false means the instance is running a different contract
// than the collector knows (a redeploy the collector hasn't re-learned yet); nil means one side
// didn't supply a hash.
type InstanceView struct {
	InstanceID       string    `json:"instanceId,omitempty"`
	ServiceVersion   string    `json:"serviceVersion,omitempty"`
	Healthy          bool      `json:"healthy"`
	LastHeartbeat    time.Time `json:"lastHeartbeat"`
	RegistrationHash string    `json:"registrationHash,omitempty"`
	HashMatches      *bool     `json:"hashMatches,omitempty"`
}

// TraceView is the benzene:mesh:query:trace response: the flow's events in start order.
type TraceView struct {
	TraceID string            `json:"traceId"`
	Events  []mesh.TraceEvent `json:"events"`
}

// ServiceQuery is the benzene:mesh:query:service request body.
type ServiceQuery struct {
	Service string `json:"service"`
}

// TopicQuery is the benzene:mesh:query:topic request body.
type TopicQuery struct {
	Topic   string `json:"topic"`
	Version string `json:"version,omitempty"`
}

// TraceQuery is the benzene:mesh:query:trace request body.
type TraceQuery struct {
	TraceID string `json:"traceId"`
}

// Collector is the meshd service: the store plus a ready-to-serve ApplicationBuilder.
type Collector struct {
	store   *store
	builder *benzene.ApplicationBuilder
}

// New builds a Collector with every benzene:mesh:* topic registered and a pipeline that also
// serves the collector's own Contract Document on the reserved benzene:spec topic (the collector is a
// Benzene service like any other).
func New(options Options) *Collector {
	if options.MaxTraceEvents <= 0 {
		options.MaxTraceEvents = 4096
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	s := newStore(options.MaxTraceEvents, options.Now)
	registry := benzene.NewRegistry()

	// The register body is a Contract Document, read loosely (mesh.md §2): only service is
	// required, and the raw body is kept for the service view.
	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicRegister),
		benzene.Handler[json.RawMessage, Ack](func(_ context.Context, body json.RawMessage) benzene.Result[Ack] {
			var doc contract.Document
			if err := json.Unmarshal(body, &doc); err != nil {
				return benzene.BadRequest[Ack]("the register body is not a contract document: " + err.Error())
			}
			if doc.Service == "" {
				return benzene.BadRequest[Ack]("service is required")
			}
			s.register(doc.Service, newRegistration(doc, body))
			return benzene.Ok(Ack{Accepted: 1})
		})))

	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicHeartbeat),
		benzene.Handler[mesh.Heartbeat, Ack](func(_ context.Context, hb mesh.Heartbeat) benzene.Result[Ack] {
			if hb.Service == "" {
				return benzene.BadRequest[Ack]("service is required")
			}
			s.heartbeat(hb)
			return benzene.Ok(Ack{Accepted: 1})
		})))

	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicTraces),
		benzene.Handler[mesh.TraceBatch, Ack](func(_ context.Context, batch mesh.TraceBatch) benzene.Result[Ack] {
			return benzene.Ok(Ack{Accepted: s.addEvents(batch.Events)})
		})))

	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicIssues),
		benzene.Handler[mesh.IssueBatch, Ack](func(_ context.Context, batch mesh.IssueBatch) benzene.Result[Ack] {
			// Batch-level service is required and attributable, even for the empty-batch liveness
			// assertion (mesh.md §4.1).
			if batch.Service == "" {
				return benzene.BadRequest[Ack]("service is required")
			}
			return benzene.Ok(Ack{Accepted: s.addIssues(batch)})
		})))

	// The fleet query takes no parameters; json.RawMessage tolerates any body, including
	// an empty one, via the router's zero-copy passthrough.
	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicQueryFleet),
		benzene.Handler[json.RawMessage, FleetView](func(_ context.Context, _ json.RawMessage) benzene.Result[FleetView] {
			return benzene.Ok(s.fleet())
		})))

	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicQueryService),
		benzene.Handler[ServiceQuery, ServiceView](func(_ context.Context, query ServiceQuery) benzene.Result[ServiceView] {
			if query.Service == "" {
				return benzene.BadRequest[ServiceView]("service is required")
			}
			view, ok := s.service(query.Service)
			if !ok {
				return benzene.NotFound[ServiceView]("unknown service " + query.Service)
			}
			return benzene.Ok(view)
		})))

	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicQueryTopic),
		benzene.Handler[TopicQuery, TopicSummary](func(_ context.Context, query TopicQuery) benzene.Result[TopicSummary] {
			if query.Topic == "" {
				return benzene.BadRequest[TopicSummary]("topic is required")
			}
			view, ok := s.topic(query.Topic, query.Version)
			if !ok {
				return benzene.NotFound[TopicSummary]("unknown topic " + query.Topic)
			}
			return benzene.Ok(view)
		})))

	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicQueryTrace),
		benzene.Handler[TraceQuery, TraceView](func(_ context.Context, query TraceQuery) benzene.Result[TraceView] {
			if query.TraceID == "" {
				return benzene.BadRequest[TraceView]("traceId is required")
			}
			view, ok := s.trace(query.TraceID)
			if !ok {
				return benzene.NotFound[TraceView]("unknown trace " + query.TraceID)
			}
			return benzene.Ok(view)
		})))

	// The collector makes no outbound calls of its own - a real (empty) OutboundRegistry asserts
	// "sends nothing" rather than leaving events and sends undetermined. Its own topics are all
	// reserved, so its document lists no requests either.
	doc, err := contract.Derive(registry, contract.NewOutboundRegistry(), contract.Info{Service: "meshd"})
	if err != nil {
		panic(err) // the collector's payload types are fixed: a collision is a bug in this package
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(contract.Middleware(doc), benzene.RouterMiddleware(registry)),
	}
	return &Collector{store: s, builder: builder}
}

// Builder returns the collector's ApplicationBuilder, ready for any transport binding -
// httpbinding.EnvelopeHandler(c.Builder()) is the usual HTTP wiring.
func (c *Collector) Builder() *benzene.ApplicationBuilder {
	return c.builder
}

// mustRegister panics on a registration error. New registers a fixed set of distinct
// topics on a fresh registry, so an error here is a programming bug in this package, not
// a runtime condition.
func mustRegister(err error) {
	if err != nil {
		panic(err)
	}
}
