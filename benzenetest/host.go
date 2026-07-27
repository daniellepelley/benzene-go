package benzenetest

import (
	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// TB is the slice of *testing.T (and *testing.B) this harness needs, declared as an interface
// so the transport Send* helpers - some of which live in separate modules (awssqs, awssns) -
// don't each have to import "testing" into their production build. *testing.T satisfies it, so
// callers just pass t.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Host is a provider-agnostic, in-memory test host: the developer's real application booted
// from its own composition root (a benzene.App), with any external edge swapped for a fake via
// WithServices. It carries the built ApplicationBuilder (registry + container + pipeline) plus
// the application's HTTP route table; a single transport-specific Send* helper turns it into a
// native event in the front door and a native response back out.
//
// The one thing a Host deliberately does NOT name is a transport or cloud - that is the job of
// the per-transport Send* call (SendSQS, SendAPIGateway, SendPubSub, ...). Lines that create the
// host, override services, and assert are identical across every transport; only the Send* call
// changes. That parallelism is the point.
type Host struct {
	builder *benzene.ApplicationBuilder
	routes  []httpbinding.Route
}

// setup accumulates the neutral, transport-independent options applied before the app's own
// Configure runs.
type setup struct {
	services []func(builder *benzene.ApplicationBuilder)
	routes   []httpbinding.Route
}

// Option configures a Host before its composition root's Configure phase runs.
type Option func(*setup)

// WithServices registers an action that runs after the app's own ConfigureServices but before
// its Configure builds the pipeline - the same ordering as the reference BenzeneTestHost -
// so a test replaces a real dependency (the outbound client, a store, a clock) with a fake and
// last-registration-wins over the app's own registration. The action receives the
// ApplicationBuilder with Registry and Container already populated (Pipeline not yet built), so
// it can reach any registration:
//
//	benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
//	    client.RegisterSender(b.Container, fake)
//	})
func WithServices(action func(builder *benzene.ApplicationBuilder)) Option {
	return func(s *setup) { s.services = append(s.services, action) }
}

// WithRoutes supplies the HTTP route table (path/method -> topic) the HTTP-shaped hosts need -
// API Gateway (SendAPIGateway) and Azure Functions HTTP triggers (SendAzureHTTP). It is the one
// piece of wiring an application declares next to its HTTP entry point in main() rather than in
// its pipeline, so a test declares the same routes here. Queue-shaped transports (SQS, SNS,
// Pub/Sub, Azure queues) route by message attribute and ignore it.
func WithRoutes(routes ...httpbinding.Route) Option {
	return func(s *setup) { s.routes = append(s.routes, routes...) }
}

// NewHost boots app from its composition root and returns a provider-agnostic Host. It runs the
// three-phase lifecycle of core-concepts.md §7 with the test seam the reference harness uses:
// GetConfiguration, then ConfigureServices, then the WithServices overrides (last-wins, before
// the pipeline exists), then Configure. Because the overrides land before Configure builds the
// pipeline - and container resolution is lazy - a faked dependency is what every handler
// resolves, exactly as in a real deployment.
func NewHost[TConfig any](app benzene.App[TConfig], opts ...Option) *Host {
	var s setup
	for _, opt := range opts {
		opt(&s)
	}

	var config TConfig
	if app.GetConfiguration != nil {
		config = app.GetConfiguration()
	}

	registry := benzene.NewRegistry()
	container := benzene.NewContainer()
	if app.ConfigureServices != nil {
		app.ConfigureServices(registry, container, config)
	}

	builder := &benzene.ApplicationBuilder{Registry: registry, Container: container}

	// Overrides run after the app's own ConfigureServices and before Configure builds the
	// pipeline - the last-registration-wins seam that lets a test fake the external edges.
	for _, action := range s.services {
		action(builder)
	}

	if app.Configure != nil {
		app.Configure(builder, config)
	}

	return &Host{builder: builder, routes: s.routes}
}

// Builder returns the built ApplicationBuilder. Transport Send* helpers in other packages read
// the Registry/Container/Pipeline off it to construct that transport's native entry point; a
// test rarely needs it directly.
func (h *Host) Builder() *benzene.ApplicationBuilder { return h.builder }

// Routes returns the HTTP route table registered with WithRoutes, for the HTTP-shaped Send*
// helpers.
func (h *Host) Routes() []httpbinding.Route { return h.routes }
