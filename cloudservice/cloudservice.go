// Package cloudservice assembles a Benzene Cloud Service (docs/specification/cloud-service-profile.md)
// from a registry in one call, wiring the reserved /benzene/* HTTP surface and reporting which
// profile surfaces the wiring provides. It is the Go form of Benzene.CloudService's builder: the
// pieces (mesh descriptor middleware, mesh.SpecHandler, healthcheck middleware, the reserved
// httpbinding paths) already exist and mesh-helloworld wires them by hand; this packages that
// assembly, with profile-conformant defaults, behind New.
//
// What it mounts on the returned http.Handler:
//
//   - EnvelopePath (/benzene/invoke) -> the wire-envelope entry point (service-to-service invoke).
//   - HealthPath   (/benzene/health) -> the reserved benzene:healthcheck topic (aggregate health).
//   - SpecPath     (/benzene/spec)   -> the registry-derived descriptor as the profile's R5 spec
//     document (unless the descriptor surface is disabled).
//   - the app's own Routes, plus the reserved benzene:mesh descriptor topic over the envelope path.
//
// Deliberately in scope: the synchronous HTTP surface + a wiring self-check. Out of scope for now
// (documented in ROADMAP): the outbound mesh feeds (register/heartbeat/traces to a collector) - drive
// those with the mesh push exporters as mesh-helloworld does; this builder does not own their
// goroutine lifecycle. The Report notes the outbound feeds as informational, not a failure.
package cloudservice

import (
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
)

// Service is a fully-assembled Cloud Service: the HTTP handler serving the reserved /benzene/* paths
// and the app routes, the derived Descriptor, the ApplicationBuilder the surface runs on, and the
// wiring self-check Report.
type Service struct {
	Handler    http.Handler
	Builder    *benzene.ApplicationBuilder
	Descriptor mesh.Descriptor
	Report     ProfileReport
}

// Option configures New.
type Option func(*config)

type config struct {
	version, instanceID, binding string
	placement                    mesh.Placement
	checks                       []healthcheck.Check
	routes                       []httpbinding.Route
	descriptor                   bool
	container                    *benzene.Container
}

// WithServiceVersion sets the service version reported in the descriptor (it participates in the
// contract hash).
func WithServiceVersion(v string) Option { return func(c *config) { c.version = v } }

// WithInstanceID sets the instance id reported in the descriptor.
func WithInstanceID(id string) Option { return func(c *config) { c.instanceID = id } }

// WithBinding sets the transport binding label reported in the descriptor (e.g. "http").
func WithBinding(b string) Option { return func(c *config) { c.binding = b } }

// WithPlacement sets the descriptor placement. Per mesh.md §2, only name a region the platform
// actually documents - never guess; leave region empty when unknown. When unset, placement is
// auto-detected.
func WithPlacement(cloud, region string) Option {
	return func(c *config) { c.placement = mesh.Placement{Cloud: cloud, Region: region} }
}

// WithHealthChecks adds health checks run for the reserved benzene:healthcheck topic (profile R3).
func WithHealthChecks(checks ...healthcheck.Check) Option {
	return func(c *config) { c.checks = append(c.checks, checks...) }
}

// WithRoutes adds the application's native-HTTP routes (topic-per-path), mounted alongside the
// reserved /benzene/* paths.
func WithRoutes(routes ...httpbinding.Route) Option {
	return func(c *config) { c.routes = append(c.routes, routes...) }
}

// WithContainer supplies the DI container the pipeline resolves scoped services from. When unset a
// fresh empty container is used.
func WithContainer(container *benzene.Container) Option {
	return func(c *config) { c.container = container }
}

// WithoutDescriptor disables the descriptor surfaces - the reserved benzene:mesh topic (R6) and the
// GET /benzene/spec document (R5) are not mounted. Use it for a deployment that must not expose its
// contract (profile §4 exposure control); the Report then marks those surfaces unsatisfied by choice.
func WithoutDescriptor() Option { return func(c *config) { c.descriptor = false } }

// New assembles a Cloud Service named serviceName from registry (with its handlers already
// registered). The reserved /benzene/* surface is wired with profile-conformant defaults; options
// adjust identity, health checks, routes, and exposure.
//
// registry must be non-nil: it backs RouterMiddleware, so an application-topic dispatch on a nil
// registry would panic (unlike mesh.Describe, which tolerates a nil registry by degrading). A
// service with no application topics is fine - pass an empty registry - but not a nil one.
func New(serviceName string, registry *benzene.Registry, opts ...Option) *Service {
	cfg := config{descriptor: true}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.container == nil {
		cfg.container = benzene.NewContainer()
	}

	info := mesh.ServiceInfo{
		Service:        serviceName,
		ServiceVersion: cfg.version,
		InstanceID:     cfg.instanceID,
		Binding:        cfg.binding,
		Placement:      cfg.placement,
	}
	descriptor := mesh.Describe(registry, info)

	// Pipeline order: descriptor interception (if enabled) and health interception both short-circuit
	// their reserved topics before the router, so a reserved topic never falls through to a missing
	// handler; the router runs last.
	var middlewares []benzene.Middleware
	if cfg.descriptor {
		middlewares = append(middlewares, mesh.Middleware(descriptor))
	}
	middlewares = append(middlewares, healthcheck.Middleware(cfg.checks), benzene.RouterMiddleware(registry))

	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: cfg.container,
		Pipeline:  benzene.NewPipeline(middlewares...),
	}

	// The reserved health topic is served over HTTP at HealthPath, ahead of the app's own routes.
	routes := append([]httpbinding.Route{
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	}, cfg.routes...)

	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	if cfg.descriptor {
		mux.Handle(httpbinding.SpecPath, mesh.SpecHandler(descriptor))
	}
	mux.Handle("/", httpbinding.Handler(builder, routes))

	return &Service{
		Handler:    mux,
		Builder:    builder,
		Descriptor: descriptor,
		Report:     buildReport(cfg),
	}
}
