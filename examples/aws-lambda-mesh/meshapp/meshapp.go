// Package meshapp is the shared composition root every one of examples/aws-lambda-mesh's six
// domain service Lambdas (orders, payments, shipping, inventory, notifications, analytics) is
// built from - the Go counterpart of benzene-typescript's compositeAwsLambda: one Lambda function
// answers whichever trigger shapes actually reach it (API Gateway HTTP, SQS, SNS, EventBridge),
// routed by the shape of the raw event JSON rather than a fixed transport, and pushes
// register/heartbeat/trace/issue reports to the mesh Lambda over a direct, synchronous Lambda
// Invoke - see examples/aws-lambda-mesh/README.md's "Divergence from .NET" section for why this
// port's mesh is push-based, reusing examples/k8s-mesh-helloworld's collector pattern with
// awslambdaclient.Client standing in for that example's httpclient.Client.
//
// # Why the pipeline is rebuilt every invocation
//
// mesh.PushExporter/PushIssueExporter batch on a background goroutine and rely on either a
// BatchSize trip or a FlushInterval tick to flush - both timers that only advance while the
// process is actually scheduled. A Lambda execution environment is FROZEN between invocations
// (not merely idle), so a long-lived exporter's ticks would often never fire before the process
// freezes, leaving that invocation's trace/issue events stuck in the queue - silently, since the
// feed is deliberately lossy by design (see mesh.PushExporter's doc comment). To make the demo
// deterministic (the same goal that motivates pinning the mesh Lambda's
// reserved_concurrent_executions to 1 - see the README), App builds a FRESH exporter pair for
// every invocation, with BatchSize 1 so the single trace/issue event from this invocation flushes
// immediately, and Close()s them (a synchronous, blocking flush) before the handler returns. Only
// the Pipeline is rebuilt this way - the Registry and the derived Descriptor (schema derivation is
// startup-only, never on the dispatch path, matching every other package in this repo) are built
// once at cold start and reused for the life of the execution environment.
package meshapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awseventbridge"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awslambdaclient"
	"github.com/daniellepelley/benzene-go/awssns"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
)

// Config wires one composite service Lambda.
type Config struct {
	// ServiceName identifies the service in the mesh (mesh.ServiceInfo.Service) and is used as
	// its InstanceID too - one instance per deployed function, so the two coincide.
	ServiceName string
	// Register runs once at cold start: register this service's domain handler(s) on registry
	// and return any HTTP routes beyond the standard GET /benzene/health, which App always adds.
	// A pure event consumer (inventory/notifications/analytics) returns nil routes - it answers
	// no HTTP traffic at all beyond its health check.
	Register func(registry *benzene.Registry) []httpbinding.Route
	// MeshClient invokes the mesh Lambda directly (awslambdaclient.Client already satisfies
	// mesh.Sender's Send signature, so it plugs into PushExporter/PushIssueExporter with no
	// adapter needed). Nil disables fleet reporting entirely - the service still answers
	// traffic, it just doesn't announce itself, matching every other reduced-mesh posture in
	// this repo.
	MeshClient *awslambdaclient.Client
}

// App is one composite service Lambda: its registry + derived descriptor (built once) plus
// everything Handler needs to serve a mixed-transport front door and report to the mesh.
type App struct {
	name       string
	registry   *benzene.Registry
	routes     []httpbinding.Route
	descriptor mesh.Descriptor
	info       mesh.ServiceInfo
	meshClient *awslambdaclient.Client
	checks     []healthcheck.Check
}

// New builds the App: registers the domain handler(s), derives the descriptor from the now-final
// registry, and wires the standard "self" health check - all startup work, done once per cold
// start.
func New(cfg Config) *App {
	registry := benzene.NewRegistry()
	routes := []httpbinding.Route{
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	}
	if cfg.Register != nil {
		routes = append(routes, cfg.Register(registry)...)
	}

	info := mesh.ServiceInfo{Service: cfg.ServiceName, ServiceVersion: "1.0.0", InstanceID: cfg.ServiceName, Binding: "aws-lambda"}
	descriptor := mesh.Describe(registry, nil, info)

	checks := []healthcheck.Check{healthcheck.NamedCheck("self", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "self", Data: map[string]any{"service": cfg.ServiceName}}
	})}

	return &App{
		name:       cfg.ServiceName,
		registry:   registry,
		routes:     routes,
		descriptor: descriptor,
		info:       info,
		meshClient: cfg.MeshClient,
		checks:     checks,
	}
}

// Descriptor returns the service's derived self-description, for tests and for Announce.
func (a *App) Descriptor() mesh.Descriptor { return a.descriptor }

// Announce registers a.Descriptor with the mesh collector, retrying with a short backoff while
// the mesh Lambda comes up (matching examples/k8s-mesh-helloworld/cmd/service's announce retry
// loop, scaled down for Lambda's tighter cold-start budget: 5 attempts instead of 30). A nil
// MeshClient is a no-op success - fleet reporting simply wasn't configured, which reduces the
// mesh, never this service. Call it once, synchronously, before awslambda.Start in main().
func (a *App) Announce(ctx context.Context) bool {
	if a.meshClient == nil {
		return true
	}
	for attempt := 0; ; attempt++ {
		if a.announceOnce(ctx) {
			return true
		}
		if attempt >= 4 {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
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
// from actually running a.checks; a static healthy report keeps the example focused, matching
// examples/k8s-mesh-helloworld/cmd/service's own heartbeat. A nil MeshClient makes this a no-op.
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

// Handler returns the composite awslambda.HandlerFunc: heartbeat the mesh (best-effort), build a
// fresh trace/issue-reporting pipeline for this one invocation (see the package doc for why),
// dispatch by the event's shape, then flush and close the exporters before returning.
func (a *App) Handler() awslambda.HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		a.heartbeat(ctx)

		var exporter *mesh.PushExporter
		var issueExporter *mesh.PushIssueExporter
		if a.meshClient != nil {
			exporter = mesh.NewPushExporter(a.meshClient, mesh.PushExporterOptions{BatchSize: 1, FlushInterval: time.Second})
			issueExporter = mesh.NewPushIssueExporter(a.meshClient, a.name, mesh.PushIssueExporterOptions{FlushInterval: time.Second})
		}
		defer exporter.Close()
		defer issueExporter.Close()

		pipeline := benzene.NewPipeline(
			mesh.TraceMiddleware(a.info, exporter),
			mesh.IssueMiddleware(a.info, issueExporter),
			mesh.Middleware(a.descriptor),
			healthcheck.Middleware(a.checks),
			benzene.RouterMiddleware(a.registry),
		)
		builder := &benzene.ApplicationBuilder{Registry: a.registry, Container: benzene.NewContainer(), Pipeline: pipeline}

		return dispatch(ctx, builder, a.routes, event)
	}
}

// shape classifies a raw Lambda event by which trigger delivered it, so one composite handler can
// answer several transports without the caller declaring which.
type shape int

const (
	shapeUnknown shape = iota
	shapeHTTP
	shapeSQS
	shapeSNS
	shapeEventBridge
)

// eventProbe reads just enough of the raw event to tell the four shapes apart, per each trigger's
// own documented payload:
//   - API Gateway (v1/v2) / Function URL: a non-empty top-level "requestContext".
//   - EventBridge rule invoke: a non-empty top-level "detail-type" (no "Records" at all).
//   - SNS-to-Lambda subscription: "Records"[0] carries a nested "Sns" object.
//   - SQS event-source mapping: "Records" present, no nested "Sns" object.
type eventProbe struct {
	RequestContext json.RawMessage `json:"requestContext"`
	DetailType     string          `json:"detail-type"`
	Records        []struct {
		Sns json.RawMessage `json:"Sns"`
	} `json:"Records"`
}

func classify(event json.RawMessage) shape {
	var p eventProbe
	if err := json.Unmarshal(event, &p); err != nil {
		return shapeUnknown
	}
	switch {
	case len(p.RequestContext) > 0:
		return shapeHTTP
	case p.DetailType != "":
		return shapeEventBridge
	case len(p.Records) > 0 && len(p.Records[0].Sns) > 0:
		return shapeSNS
	case len(p.Records) > 0:
		return shapeSQS
	default:
		return shapeUnknown
	}
}

// dispatch routes event to the awslambda adapter matching its shape. Each adapter already
// satisfies awslambda's "never return a Go error to the transport unless the platform's own
// redelivery/retry mechanism should see it" rule on its own; dispatch adds nothing but the
// routing decision.
func dispatch(ctx context.Context, builder *benzene.ApplicationBuilder, routes []httpbinding.Route, event json.RawMessage) (json.RawMessage, error) {
	switch classify(event) {
	case shapeHTTP:
		return awslambda.HTTPHandler(builder, routes)(ctx, event)
	case shapeSQS:
		return awssqs.Handler(builder)(ctx, event)
	case shapeSNS:
		return awssns.Handler(builder)(ctx, event)
	case shapeEventBridge:
		return awseventbridge.Handler(builder)(ctx, event)
	default:
		return nil, fmt.Errorf("meshapp: unrecognised event shape")
	}
}
