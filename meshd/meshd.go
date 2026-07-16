// Package meshd implements Phases 3-4 of the Benzene Mesh design (docs/design/mesh.md):
// the collector. It is itself an ordinary Benzene service - its mesh:* topics live in a
// Registry, are served through a Pipeline, and it serves its own descriptor on the
// reserved mesh topic - so deploying it anywhere this module's bindings reach (Lambda,
// Functions, Cloud Run, plain HTTP) is the same exercise as deploying any other service.
//
// The collector accepts partial fleets, mirroring the mesh package's degradation rule:
// traces from a service that never registered render it anonymous-but-live (its row shows
// the missing descriptor feed); a registered service with no traffic is a catalog entry
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

// FleetView is the mesh:query:fleet response: the whole known fleet in one shape - what
// the Mesh View renders (mesh.md §6.1/§6.2/§6.4).
type FleetView struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Services    []ServiceSummary `json:"services"`
	Topics      []TopicSummary   `json:"topics"`
	Traces      []TraceSummary   `json:"traces"`
}

// Health classification of a service, from its instances' latest heartbeats.
const (
	healthHealthy  = "healthy"
	healthDegraded = "degraded"
	healthUnknown  = "unknown"
)

// ServiceSummary is one service's fleet row. MissingFeeds names the feeds the collector
// has not received for it ("descriptor", "health", "traces") - a reduced service is shown
// as reduced, never mistaken for an empty or dead one.
type ServiceSummary struct {
	Service      string         `json:"service"`
	Runtime      string         `json:"runtime,omitempty"`
	Binding      string         `json:"binding,omitempty"`
	Placement    mesh.Placement `json:"placement"`
	Topics       int            `json:"topics"`
	Instances    int            `json:"instances"`
	Health       string         `json:"health"`
	LastSeen     time.Time      `json:"lastSeen"`
	Invocations  int64          `json:"invocations"`
	Errors       int64          `json:"errors"`
	MissingFeeds []string       `json:"missingFeeds,omitempty"`
}

// TopicSummary is one topic's catalog row: providers come from descriptors, consumers
// from observed trace parentage, stats from the trace feed - nothing is declared.
type TopicSummary struct {
	Topic         string           `json:"topic"`
	Version       string           `json:"version,omitempty"`
	Providers     []string         `json:"providers,omitempty"`
	Consumers     []string         `json:"consumers,omitempty"`
	Invocations   int64            `json:"invocations"`
	Errors        int64            `json:"errors"`
	AvgDurationMs float64          `json:"avgDurationMs"`
	StatusCounts  map[string]int64 `json:"statusCounts,omitempty"`
	LastSeen      time.Time        `json:"lastSeen"`
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

// ServiceView is the mesh:query:service response.
type ServiceView struct {
	ServiceSummary
	Descriptor *mesh.Descriptor `json:"descriptor,omitempty"`
	Instances  []InstanceView   `json:"instances,omitempty"`
}

// InstanceView is one instance's latest heartbeat state. HashMatches compares the
// heartbeat's descriptor hash against the registered descriptor's - false means the
// instance is running a different contract than the collector knows (a redeploy the
// collector hasn't re-learned yet); nil means one side didn't supply a hash.
type InstanceView struct {
	InstanceID     string    `json:"instanceId,omitempty"`
	Healthy        bool      `json:"healthy"`
	LastHeartbeat  time.Time `json:"lastHeartbeat"`
	DescriptorHash string    `json:"descriptorHash,omitempty"`
	HashMatches    *bool     `json:"hashMatches,omitempty"`
}

// TraceView is the mesh:query:trace response: the flow's events in start order.
type TraceView struct {
	TraceID string            `json:"traceId"`
	Events  []mesh.TraceEvent `json:"events"`
}

// ServiceQuery is the mesh:query:service request body.
type ServiceQuery struct {
	Service string `json:"service"`
}

// TopicQuery is the mesh:query:topic request body.
type TopicQuery struct {
	Topic   string `json:"topic"`
	Version string `json:"version,omitempty"`
}

// TraceQuery is the mesh:query:trace request body.
type TraceQuery struct {
	TraceID string `json:"traceId"`
}

// Collector is the meshd service: the store plus a ready-to-serve ApplicationBuilder.
type Collector struct {
	store   *store
	builder *benzene.ApplicationBuilder
}

// New builds a Collector with every mesh:* topic registered and a pipeline that also
// serves the collector's own descriptor on the reserved mesh topic (the collector is a
// Benzene service like any other, and appears in its own catalog).
func New(options Options) *Collector {
	if options.MaxTraceEvents <= 0 {
		options.MaxTraceEvents = 4096
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	s := newStore(options.MaxTraceEvents, options.Now)
	registry := benzene.NewRegistry()

	mustRegister(benzene.Register(registry, benzene.NewTopic(mesh.TopicRegister),
		benzene.Handler[mesh.Descriptor, Ack](func(_ context.Context, desc mesh.Descriptor) benzene.Result[Ack] {
			if desc.Service == "" {
				return benzene.BadRequest[Ack]("service is required")
			}
			s.register(desc)
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

	descriptor := mesh.Describe(registry, mesh.ServiceInfo{Service: "meshd"})
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(mesh.Middleware(descriptor), benzene.RouterMiddleware(registry)),
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
