package benzene

import "github.com/daniellepelley/benzene-go/wire"

// App is a Benzene application definition: the three-phase lifecycle of
// core-concepts.md §7, run once, in order, at startup:
//
//  1. GetConfiguration produces the configuration object. No service resolution is
//     available yet.
//  2. ConfigureServices registers handlers, middleware dependencies, and adapters with
//     the registry/container.
//  3. Configure builds the pipeline(s) against a platform-neutral ApplicationBuilder.
//     Transport-specific entry points are attached by calling a transport binding's own
//     constructor against the returned ApplicationBuilder.
//
// TConfig is application-defined; Benzene itself doesn't prescribe its shape.
type App[TConfig any] struct {
	GetConfiguration  func() TConfig
	ConfigureServices func(registry *Registry, container *Container, config TConfig)
	Configure         func(builder *ApplicationBuilder, config TConfig)
}

// Run executes the three-phase lifecycle once and returns the built ApplicationBuilder,
// ready for a transport binding to attach entry points to (e.g. an http.Handler for the
// HTTP binding). ConfigureServices and Configure are optional - an application with no
// dependencies to register, or nothing further to configure beyond the defaults, may leave
// either nil.
func (a App[TConfig]) Run() *ApplicationBuilder {
	config := a.GetConfiguration()

	registry := NewRegistry()
	container := NewContainer()
	if a.ConfigureServices != nil {
		a.ConfigureServices(registry, container, config)
	}

	builder := &ApplicationBuilder{Registry: registry, Container: container}
	if a.Configure != nil {
		a.Configure(builder, config)
	}
	return builder
}

// ApplicationBuilder is the platform-neutral application builder handed to App.Configure.
// A transport binding's `Use<Transport>(builder, ...)`-shaped constructor reads Registry/
// Container/Pipeline off it to build that transport's native entry point (an http.Handler,
// a Lambda handler function, ...) - core-concepts.md §7's "one application definition can
// target several platforms" rule. Go typically compiles one binary per deployment target
// rather than runtime-detecting the host, so the "no-op on other platforms" half of that
// rule mostly falls out for free here; a future binding that DOES need runtime platform
// detection (e.g. a single binary that can run as either an HTTP server or a Lambda
// function depending on environment) can still check for its own platform indicators before
// activating, exactly as any other Go code would.
type ApplicationBuilder struct {
	Registry  *Registry
	Container *Container
	Pipeline  *Pipeline
	// ReservedNames overrides the reserved metadata/header names (wire-contracts.md §2). It is
	// the single injectable value the spec calls for: set it once here and every inbound binding
	// built off this builder reads it, so a service renames a colliding key in one place. Its
	// zero value means the standard defaults. The same value MUST also be given to the service's
	// outbound clients (the queue Client structs' ReservedNames field), since an override applies
	// to both directions.
	ReservedNames wire.ReservedNames
}

// UsePipeline sets the middleware pipeline transport bindings will run invocations through.
// Call this from Configure before any binding constructor that needs it. Returns the
// builder so calls can be chained.
func (b *ApplicationBuilder) UsePipeline(pipeline *Pipeline) *ApplicationBuilder {
	b.Pipeline = pipeline
	return b
}

// UseReservedNames overrides the reserved metadata/header names (wire-contracts.md §2) for every
// inbound binding built off this builder. Call it from Configure before the binding constructors.
// Returns the builder so calls can be chained. Remember to pass the same names to the service's
// outbound clients - an override applies to both directions.
func (b *ApplicationBuilder) UseReservedNames(names wire.ReservedNames) *ApplicationBuilder {
	b.ReservedNames = names
	return b
}
