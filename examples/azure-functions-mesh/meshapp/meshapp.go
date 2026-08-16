// Package meshapp is the shared composition root every one of examples/azure-functions-mesh's
// seven Azure Functions (orders, payments, shipping, inventory, notifications, analytics, and the
// mesh itself) is built from - the Go counterpart of examples/aws-lambda-mesh/meshapp.App, adapted
// for Azure Functions' custom-handler HTTP dispatch instead of Lambda's composite-event-shape
// dispatch, and for plain-HTTP push (httpclient.Client) instead of a direct Lambda Invoke. See
// examples/azure-functions-mesh/README.md's "Divergence from .NET" section for why this port's
// mesh is push-based at all.
//
// # Why the fleet-reporting pipeline is built ONCE, unlike examples/aws-lambda-mesh's App
//
// examples/aws-lambda-mesh/meshapp.App rebuilds a fresh trace/issue-exporter pair on EVERY
// invocation, because a Lambda execution environment is FROZEN (not merely idle) between
// invocations, so a long-lived exporter's background flush ticks would often never fire. An Azure
// Functions custom handler is a different execution model: the host starts the handler executable
// once and keeps it running as an ordinary, persistent HTTP server for the life of the warm
// instance (Consumption-plan cold starts aside) - much closer to
// examples/k8s-mesh-helloworld/cmd/service's always-on process than to Lambda's per-invocation
// freeze/thaw. So App builds its mesh.PushExporter/PushIssueExporter pair ONCE at startup and lets
// their own background goroutines batch and flush on their normal timers, exactly like
// examples/k8s-mesh-helloworld/cmd/service does - no per-invocation rebuild, no BatchSize:1
// workaround.
//
// # The Cloud Service Profile surface on a non-forwarding custom handler
//
// host.json here sets customHandler.enableForwardingHttpRequest to false (matching
// examples/azure-functions-helloworld's own default), so every HTTP-triggered Azure Function
// folder invokes the SAME shared azurefunctions.Handler at its own fixed *local* invocation path
// (see azurefunctions.Handler's doc comment) - meaning /benzene/spec and /benzene/health are
// ordinary topic-routed Routes (mesh.TopicID intercepted by mesh.Middleware, and
// healthcheck.ReservedTopic intercepted by healthcheck.Middleware) dispatched exactly like any
// other domain topic. /benzene/invoke does not fit that Route-per-topic table at all - its topic
// travels in the request BODY, not a fixed route - so it and the mesh Function's Fleet View need
// their own small adapters; see httpadapters.go's package doc for why those live here, as
// example-local glue, rather than as azurefunctions framework code.
package meshapp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"
)

// Config wires one composite service Function App.
type Config struct {
	// ServiceName identifies the service in the mesh (mesh.ServiceInfo.Service) and is used as
	// its InstanceID too - one instance per deployed Function App, so the two coincide.
	ServiceName string
	// Register runs once at startup: register this service's domain handler(s) on registry -
	// what it RECEIVES - and its outbound records on outbound - what it SENDS (mesh.md §2.3,
	// domain.RegisterOutbound) - then return any HTTP routes beyond the standard
	// GET /Spec -> benzene:mesh and GET /Health -> benzene:healthcheck routes, which New always
	// adds. A pure event consumer (inventory/notifications/analytics) registers nothing outbound
	// and returns nil routes - its only HTTP surface is the standard Cloud Service Profile one,
	// everything else arrives over Service Bus/Event Hub/Event Grid triggers mounted directly in
	// cmd/<service>/main.go.
	//
	// Both registries reach the one callback (matching examples/k8s-mesh-helloworld/cmd/service's
	// registerDomain) so each service declares both halves of its contract in the same place.
	Register func(registry *benzene.Registry, outbound *mesh.OutboundRegistry) []httpbinding.Route
	// MeshClient pushes register/heartbeat/trace/issue reports to the mesh Function's
	// POST /benzene/invoke over plain HTTP (httpclient.Client already satisfies mesh.Sender's
	// Send signature, so it plugs into PushExporter/PushIssueExporter with no adapter needed -
	// see ../README.md's "Divergence from .NET" section). Nil disables fleet reporting entirely -
	// the service still answers traffic, it just doesn't announce itself, matching every other
	// reduced-mesh posture in this repo.
	MeshClient *httpclient.Client
}

// App is one composite service Function App: its registry + derived descriptor (built once) plus
// everything main() needs to serve the standard Cloud Service Profile surface and report to the
// mesh.
type App struct {
	name          string
	registry      *benzene.Registry
	routes        []httpbinding.Route
	builder       *benzene.ApplicationBuilder
	descriptor    mesh.Descriptor
	info          mesh.ServiceInfo
	meshClient    *httpclient.Client
	exporter      *mesh.PushExporter
	issueExporter *mesh.PushIssueExporter
}

// New builds the App: registers the domain handler(s) and the service's outbound records, derives
// the descriptor from the now-final registries, wires the standard "self" health check, and - when
// cfg.MeshClient is set - starts the background trace/issue exporters. All startup work, done once.
//
// The OutboundRegistry is always real (never nil), even for a service that declares nothing: a
// present-but-empty one describes a service that genuinely sends nothing, whereas a nil one would
// mark the feed Degraded - "send side not wired up" - which is a different claim entirely.
func New(cfg Config) *App {
	registry := benzene.NewRegistry()
	outbound := mesh.NewOutboundRegistry()
	routes := []httpbinding.Route{
		{Method: http.MethodGet, Path: "/Spec", Topic: benzene.NewTopic(mesh.TopicID)},
		{Method: http.MethodGet, Path: "/Health", Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	}
	if cfg.Register != nil {
		routes = append(routes, cfg.Register(registry, outbound)...)
	}

	info := mesh.ServiceInfo{Service: cfg.ServiceName, ServiceVersion: "1.0.0", InstanceID: cfg.ServiceName, Binding: "azure-functions"}
	descriptor := mesh.Describe(registry, outbound, info)

	checks := []healthcheck.Check{healthcheck.NamedCheck("self", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "self", Data: map[string]any{"service": cfg.ServiceName}}
	})}

	var exporter *mesh.PushExporter
	var issueExporter *mesh.PushIssueExporter
	if cfg.MeshClient != nil {
		exporter = mesh.NewPushExporter(cfg.MeshClient, mesh.PushExporterOptions{FlushInterval: 5 * time.Second})
		issueExporter = mesh.NewPushIssueExporter(cfg.MeshClient, cfg.ServiceName, mesh.PushIssueExporterOptions{FlushInterval: 5 * time.Second})
	}

	pipeline := benzene.NewPipeline(
		mesh.TraceMiddleware(info, exporter),
		mesh.IssueMiddleware(info, issueExporter),
		mesh.Middleware(descriptor),
		healthcheck.Middleware(checks),
		benzene.RouterMiddleware(registry),
	)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: pipeline}

	return &App{
		name:          cfg.ServiceName,
		registry:      registry,
		routes:        routes,
		builder:       builder,
		descriptor:    descriptor,
		info:          info,
		meshClient:    cfg.MeshClient,
		exporter:      exporter,
		issueExporter: issueExporter,
	}
}

// Builder returns the ApplicationBuilder every custom-handler trigger adapter
// (azurefunctions.Handler/QueueHandler/EventGridHandler/EventHubHandler, and this package's own
// EnvelopeHandler) dispatches through.
func (a *App) Builder() *benzene.ApplicationBuilder { return a.builder }

// Descriptor returns the service's derived self-description, for tests and for Announce.
func (a *App) Descriptor() mesh.Descriptor { return a.descriptor }

// HTTPHandler builds the custom-handler HTTP server for every Route this App knows about (the
// standard Spec/Health routes plus whatever Config.Register added) - mount it at each
// corresponding Function folder's local path, e.g. mux.Handle("/Spec", app.HTTPHandler()).
func (a *App) HTTPHandler() http.Handler {
	return azurefunctions.Handler(a.builder, a.routes)
}

// EnvelopeHandler builds the custom-handler HTTP server for the wire-envelope endpoint
// (POST /benzene/invoke) - see httpadapters.go's package doc for why this needs its own adapter
// distinct from HTTPHandler's Route-table dispatch.
func (a *App) EnvelopeHandler() http.Handler {
	return EnvelopeHandler(a.builder)
}

// Close flushes and stops the background trace/issue exporters (nil-safe, a no-op when
// Config.MeshClient was nil). Call it on shutdown so the tail of the trace/issue feed isn't lost
// with the process - in practice this only runs on a graceful local shutdown; a Consumption-plan
// instance being recycled does not get the chance, matching every other push-based mesh example's
// same at-most-once-on-shutdown caveat.
func (a *App) Close() {
	a.exporter.Close()
	a.issueExporter.Close()
}

// Announce registers a.Descriptor with the mesh collector, retrying with a short backoff while
// the mesh Function comes up (matching examples/k8s-mesh-helloworld/cmd/service's own announce
// retry loop: 30 attempts, 2s apart - Azure Functions custom handlers are long-lived processes
// like that example's pods, not Lambda's frozen-between-invocations model, so the same generous
// budget fits). A nil MeshClient is a no-op success - fleet reporting simply wasn't configured,
// which reduces the mesh, never this service.
func (a *App) Announce(ctx context.Context) bool {
	if a.meshClient == nil {
		return true
	}
	for attempt := 0; ; attempt++ {
		if a.announceOnce(ctx) {
			return true
		}
		if attempt >= 29 {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *App) announceOnce(ctx context.Context) bool {
	body, err := json.Marshal(a.descriptor)
	if err != nil {
		return false
	}
	result := a.meshClient.Send(ctx, benzene.NewTopic(mesh.TopicRegister), nil, body)
	return result.IsSuccessful()
}

// heartbeat sends one health report to the mesh collector. In a real service Health would come
// from actually running the service's own checks; a static healthy report keeps the example
// focused, matching every other push-based mesh example's own heartbeat.
func (a *App) heartbeat(ctx context.Context) {
	if a.meshClient == nil {
		return
	}
	hb, err := json.Marshal(mesh.Heartbeat{
		Service:        a.descriptor.Service,
		InstanceID:     a.descriptor.InstanceID,
		DescriptorHash: a.descriptor.DescriptorHash,
		SentAt:         time.Now().UTC(),
		Health:         healthcheck.Response{IsHealthy: true, HealthChecks: map[string]healthcheck.CheckResult{}},
	})
	if err != nil {
		return
	}
	a.meshClient.Send(ctx, benzene.NewTopic(mesh.TopicHeartbeat), nil, hb)
}

// RunHeartbeatLoop announces (retrying while the mesh Function comes up), then heartbeats every
// 10s until ctx is cancelled - call it in a background goroutine from main(), exactly like
// examples/k8s-mesh-helloworld/cmd/service's announceAndHeartbeat. A no-op when Config.MeshClient
// was nil. Like that example, the loop proceeds to heartbeating even if every announce attempt
// failed (heartbeat is equally best-effort/logged-only) rather than giving up on the service
// entirely - if the mesh Function comes up later, a subsequent heartbeat can still land once
// Announce is retried some other way (e.g. a future request context), matching the "reduces the
// mesh, never this service" degradation rule.
func (a *App) RunHeartbeatLoop(ctx context.Context) {
	if a.meshClient == nil {
		return
	}
	a.Announce(ctx)
	a.heartbeat(ctx)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.heartbeat(ctx)
		}
	}
}
