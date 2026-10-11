// Package cloudservice assembles a Benzene Cloud Service (docs/specification/cloud-service-profile.md)
// from a registry in one call, wiring the reserved /benzene/* HTTP surface and reporting which
// profile surfaces the wiring provides. It is the Go form of Benzene.CloudService's builder: the
// pieces (contract.Derive, contract.Middleware and httpbinding.SpecHandler, healthcheck middleware,
// the reserved httpbinding paths) already exist and mesh-helloworld wires them by hand; this
// packages that assembly, with profile-conformant defaults, behind New.
//
// What it mounts on the returned http.Handler:
//
//   - EnvelopePath (/benzene/invoke) -> the wire-envelope entry point (service-to-service invoke).
//   - HealthPath   (/benzene/health) -> the reserved benzene:healthcheck topic (aggregate health).
//   - SpecPath     (/benzene/spec)   -> the reserved benzene:spec topic, answered with the Contract
//     Document (the profile's R5), through the pipeline (unless the descriptor surface is disabled).
//   - the app's own Routes, plus the reserved benzene:spec topic over the envelope path.
//
// The derived Contract Document carries the wiring self-check as its profile (contract-document.md
// §1): {"name":"cloud-service","missing":[...]} - so benzene:spec, and a registration of the same
// document, say which requirements this wiring knows it does not meet.
//
// Deliberately in scope: the synchronous HTTP surface + a wiring self-check. Out of scope for now
// (documented in ROADMAP): the outbound mesh feeds (register/heartbeat/traces to a collector) - run
// a mesh.Announcer over Service.Contract and mesh.TraceMiddleware with a PushExporter, as
// mesh-helloworld does; this builder does not own their goroutine lifecycle. The Report records
// those feeds as not wired, so R6 stays unsatisfied for a New-only build.
package cloudservice

import (
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
)

// Service is a fully-assembled Cloud Service: the HTTP handler serving the reserved /benzene/* paths
// and the app routes, the derived Contract Document (what benzene:spec serves, and the register body
// a mesh.Announcer sends), the ApplicationBuilder the surface runs on, and the wiring self-check
// Report.
type Service struct {
	Handler  http.Handler
	Builder  *benzene.ApplicationBuilder
	Contract contract.Document
	Report   ProfileReport
}

// Option configures New.
type Option func(*config)

type config struct {
	version, versionScheme string
	description            string
	placement              *contract.Placement
	checks                 []healthcheck.Check
	routes                 []httpbinding.Route
	descriptor             bool
	container              *benzene.Container
	outbound               *contract.OutboundRegistry
}

// WithServiceVersion sets the service version reported in the Contract Document (outside both of its
// hashes).
func WithServiceVersion(v string) Option { return func(c *config) { c.version = v } }

// WithVersionScheme sets the comparison scheme of the service version ("semver", ...), carried by
// the Contract Document; required for an orderable version (service-versions.md §2).
func WithVersionScheme(scheme string) Option { return func(c *config) { c.versionScheme = scheme } }

// WithDescription sets the Contract Document's free-text description.
func WithDescription(description string) Option {
	return func(c *config) { c.description = description }
}

// WithPlacement sets the Contract Document's placement. Per contract-document.md §1, only name a
// region the platform actually documents - never guess; leave region empty when unknown. When unset,
// placement is detected (mesh.DetectPlacement).
func WithPlacement(cloud, region string) Option {
	return func(c *config) { c.placement = &contract.Placement{Cloud: cloud, Region: region} }
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

// WithProduces supplies the service's event and send declarations (contract.RegisterEvent,
// contract.RegisterSend): the Contract Document's events and sends - a declared contract, not an
// inference. When unset, the document lists no events or sends and names both in degraded, rather
// than asserting that the service publishes and sends nothing.
func WithProduces(outbound *contract.OutboundRegistry) Option {
	return func(c *config) { c.outbound = outbound }
}

// WithConsumes is the former name of [WithProduces], kept so existing composition roots still
// compile.
//
// Deprecated: the 2026-08 role inversion (mesh.md §4) made a service's outbound registration its
// PRODUCER claim, so the old name says the opposite of what the option does. Use [WithProduces].
func WithConsumes(outbound *contract.OutboundRegistry) Option { return WithProduces(outbound) }

// WithoutDescriptor disables the self-description surface: the reserved benzene:spec topic and its
// GET /benzene/spec route (R5, and the self-description half of R6) are not mounted. Use it for a
// deployment that must not expose its contract (profile §4 exposure control); the Report then marks
// those requirements unsatisfied by choice. Service.Contract is still derived.
func WithoutDescriptor() Option { return func(c *config) { c.descriptor = false } }

// New assembles a Cloud Service named serviceName from registry (with its handlers already
// registered). The reserved /benzene/* surface is wired with profile-conformant defaults; options
// adjust identity, health checks, routes, and exposure.
//
// It derives the Contract Document from registry and the WithProduces declarations, and returns
// that derivation's error - a component-name collision (*contract.NameCollisionError), an empty
// serviceName, or a nil registry - before anything is served.
//
// registry must be non-nil: it backs RouterMiddleware. A service with no application topics is
// fine - pass an empty registry - but not a nil one.
func New(serviceName string, registry *benzene.Registry, opts ...Option) (*Service, error) {
	cfg := config{descriptor: true}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.container == nil {
		cfg.container = benzene.NewContainer()
	}

	placement := cfg.placement
	if placement == nil {
		detected := mesh.DetectPlacement()
		placement = &detected
	}
	report := buildReport(cfg)

	// The app's own routes are the document's httpMappings; the reserved health route is not a
	// request the document lists.
	var httpRoutes []contract.HTTPRoute
	for _, r := range cfg.routes {
		httpRoutes = append(httpRoutes, contract.HTTPRoute{Method: r.Method, Path: r.Path, Topic: r.Topic})
	}
	doc, err := contract.Derive(registry, cfg.outbound, contract.Info{
		Service:         serviceName,
		ServiceVersion:  cfg.version,
		VersionScheme:   cfg.versionScheme,
		Description:     cfg.description,
		MessageEndpoint: httpbinding.EnvelopePath,
		Transports:      []string{"http"},
		Placement:       placement,
		Profile:         report.Profile(),
		HTTPRoutes:      httpRoutes,
	})
	if err != nil {
		return nil, err
	}

	// Pipeline order: the self-description interception (if enabled) and health interception all
	// short-circuit their reserved topics before the router, so a reserved topic never falls through
	// to a missing handler; the router runs last.
	var middlewares []benzene.Middleware
	if cfg.descriptor {
		middlewares = append(middlewares, contract.Middleware(doc))
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
		mux.Handle(httpbinding.SpecPath, httpbinding.SpecHandler(builder))
	}
	mux.Handle("/", httpbinding.Handler(builder, routes))

	return &Service{
		Handler:  mux,
		Builder:  builder,
		Contract: doc,
		Report:   report,
	}, nil
}
