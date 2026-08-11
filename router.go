package benzene

import (
	"context"
	"fmt"

	"github.com/daniellepelley/benzene-go/wire"
)

// RouterOption configures RouterMiddleware.
type RouterOption func(*routerConfig)

type routerConfig struct {
	versionKeys []string
}

// WithVersionKeys sets the ordered fallback list of header names the inbound payload schema
// version is read from (versioning.md §2.1) - first present wins, matched case-insensitively.
// Pass a service's ApplicationBuilder.ReservedNames.Version() so a reserved-name override made
// once via UseReservedNames drives routing here too, or a literal list to narrow or replace it
// (e.g. WithVersionKeys("benzene-version") when a producer already emits a "version" header
// meaning something unrelated). Unset, RouterMiddleware uses wire.DefaultVersionKeys.
func WithVersionKeys(keys ...string) RouterOption {
	return func(c *routerConfig) { c.versionKeys = keys }
}

// RouterMiddleware returns the terminal middleware that resolves ic.Topic against registry
// and dispatches to the matching handler, storing the outcome on ic.Result. Conventionally
// registered last in a Pipeline (core-concepts.md §4).
//
// It reads the message's payload schema version off the wire (wire-contracts.md §2 tier C,
// versioning.md §2.1) when a binding has not already resolved one: the version travels as a
// header on every transport (queues, the envelope, and the native HTTP binding's request
// headers all land it on ic.Headers), so reading it here covers them uniformly. A binding that
// resolves a version another way - e.g. an HTTP /v{version} route segment setting ic.Topic.Version
// before the pipeline runs - wins, since a version already on the topic is left untouched.
//
// Handler selection stays exact-match (core-concepts.md §2), with one fallback: a signalled
// version that has no exact (id, version) handler routes to the unversioned (default-version)
// handler if one exists. That is core-concepts.md §2's absent-means-default applied to an
// unmatched version, and the guarantee that turning on the read path stays non-regressive - a
// stray version header on a service that registered only unversioned handlers still routes to
// them rather than falling to not-found. versioning.md §3's richer exact-else-highest-supported
// selection is a deliberate future addition (see the port ROADMAP), not implemented here.
//
// Per core-concepts.md §2/§5, this middleware never returns a Go error for an application-
// level outcome - a missing topic, a missing handler, a request-conversion failure, or a
// handler panic all become a Result on ic.Result (ValidationError, NotFound, BadRequest, and
// ServiceUnavailable respectively), so every caller uniformly reads ic.Result rather than
// distinguishing "no handler" from "handler ran" via the Go error return. A handler panic
// specifically MUST NOT crash the transport adapter (§5) - recovered here and mapped to
// ServiceUnavailable, which wire-contracts.md §3 defines as "also the mapping for uncaught
// handler exceptions."
func RouterMiddleware(registry *Registry, opts ...RouterOption) Middleware {
	var cfg routerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	versionKeys := cfg.versionKeys
	if len(versionKeys) == 0 {
		versionKeys = wire.DefaultVersionKeys
	}

	return func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) (err error) {
		if ic.Topic.ID == "" {
			ic.Result = ValidationError[any]("topic is missing")
			return next(ctx)
		}

		// Read the payload schema version off the wire unless the binding already resolved one.
		if ic.Topic.Version == "" {
			ic.Topic.Version = wire.ResolveVersion(ic.Headers, versionKeys)
		}

		handler, ok := registry.resolve(ic.Topic)
		if !ok && ic.Topic.Version != "" {
			// A signalled version with no exact handler falls back to the unversioned one.
			handler, ok = registry.resolve(NewTopic(ic.Topic.ID))
		}
		if !ok {
			ic.Result = NotFound[any](fmt.Sprintf("no handler found for topic %q", ic.Topic))
			return next(ctx)
		}

		handlerCtx := contextWithInvocation(ctx, ic)
		if ic.Scope != nil {
			handlerCtx = ContextWithScope(handlerCtx, ic.Scope)
		}
		ic.Result = dispatch(handlerCtx, handler, ic.Request)
		return next(ctx)
	}
}

// dispatch calls handler with panic recovery, so a handler panic becomes a
// ServiceUnavailable result instead of crashing the pipeline (core-concepts.md §5).
func dispatch(ctx context.Context, handler erasedHandler, request any) (result ResultInfo) {
	defer func() {
		if r := recover(); r != nil {
			result = ServiceUnavailable[any](fmt.Sprintf("handler panicked: %v", r))
		}
	}()

	res, err := handler(ctx, request)
	if err != nil {
		return BadRequest[any](err.Error())
	}
	return res
}
