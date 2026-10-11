package benzene

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/daniellepelley/benzene-go/wire"
)

// PingTopic is the reserved reachability probe (wire-contracts.md §7): a health check sends it into
// a queue or a function to prove the path to a service works, and the answer is ok with no payload.
// RouterMiddleware serves it, so every service answers it with no wiring - which matters most on a
// one-way transport, where an unanswered probe would be a not-found failure the transport redelivers
// until it dead-letters it. Like every reserved topic it is matched by id alone, ignoring version.
const PingTopic = "benzene:ping"

// RouterOption configures RouterMiddleware.
type RouterOption func(*routerConfig)

type routerConfig struct {
	versionKey string
}

// WithVersionKey sets the header the message's version is read from (wire-contracts.md §2) when a
// binding has not already resolved one. Pass a service's ApplicationBuilder.ReservedNames.Version()
// so a reserved-name override made once via UseReservedNames drives routing too -
// ApplicationBuilder.UseDefaultPipeline does exactly that. Unset, RouterMiddleware reads
// wire.DefaultVersionKey ("benzene-version"). There is one name, never a list: a header under any
// other name is an ordinary header and selects nothing.
func WithVersionKey(key string) RouterOption {
	return func(c *routerConfig) { c.versionKey = key }
}

// RouterMiddleware returns the terminal middleware that resolves ic.Topic against registry
// and dispatches to the matching handler, storing the outcome on ic.Result. Conventionally
// registered last in a Pipeline (core-concepts.md §4).
//
// It reads the message's version off the wire (wire-contracts.md §2, the benzene-version header)
// when a binding has not already resolved one: the version travels as a header on every transport
// (queues, the envelope, and the native HTTP binding's request headers all land it on ic.Headers),
// so reading it here covers them uniformly. A binding that resolves a version another way - an HTTP
// /v{version} route segment setting ic.Topic.Version before the pipeline runs - wins, since a
// version already on the topic is left untouched.
//
// Handler selection is core-concepts.md §2's: a message carrying a version runs the handler
// registered for exactly that (id, version), and a message without one runs the topic's default
// handler - the unversioned handler, else the one registered with AsDefault. Anything else is
// not-found, with an error naming the topic and the version: there is no "highest available"
// fallback, and no fallback from an unknown version to the unversioned handler. This port ships no
// payload casting, so the spec's cast-reachable step never applies. When it is built, RouterMiddleware
// logs a warning (slog.Default) for each topic whose handlers are all versioned with none declared
// default (Registry.TopicsWithoutDefault), since every unversioned message to it will be not-found.
//
// The reserved PingTopic is answered ok with no payload unless the registry serves it itself.
//
// Per core-concepts.md §2/§5, this middleware never returns a Go error for an application-
// level outcome - a missing topic, a missing handler, a request-conversion failure, or a
// handler panic all become a Result on ic.Result (ValidationError, NotFound, BadRequest, and
// ServiceUnavailable respectively), so every caller uniformly reads ic.Result rather than
// distinguishing "no handler" from "handler ran" via the Go error return. A handler panic
// specifically MUST NOT crash the transport adapter (§5) - recovered here and mapped to
// ServiceUnavailable, which wire-contracts.md §3 defines as "also the mapping for uncaught
// handler exceptions." A topic no handler serves at any version is the not-found IsUnmatchedTopic
// recognizes, which is what lets a one-way binding acknowledge it on request.
func RouterMiddleware(registry *Registry, opts ...RouterOption) Middleware {
	var cfg routerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	versionKey := cfg.versionKey
	if versionKey == "" {
		versionKey = wire.DefaultVersionKey
	}
	for _, id := range registry.TopicsWithoutDefault() {
		slog.Warn("benzene: topic has only versioned handlers and none is declared default, so a message without a version will be not-found; register one with benzene.AsDefault()",
			"topic", id)
	}

	return func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) (err error) {
		if ic.Topic.ID == "" {
			ic.Result = ValidationError[any]("topic is missing")
			return next(ctx)
		}

		// Read the message's version off the wire unless the binding already resolved one.
		if ic.Topic.Version == "" {
			ic.Topic.Version = wire.ResolveVersion(ic.Headers, versionKey)
		}

		handler, found := registry.lookup(ic.Topic)
		switch found {
		case selected:
		case unknownTopic:
			if ic.Topic.ID == PingTopic {
				ic.Result = Result[any]{Status: StatusOk}
			} else {
				ic.Result = unmatched(fmt.Sprintf("no handler found for topic %q", ic.Topic.ID))
			}
			return next(ctx)
		case unknownVersion:
			ic.Result = NotFound[any](fmt.Sprintf("no handler found for topic %q at version %q", ic.Topic.ID, ic.Topic.Version))
			return next(ctx)
		default: // noDefault
			ic.Result = NotFound[any](fmt.Sprintf("no handler found for topic %q without a version: its handlers are all versioned and none is the default", ic.Topic.ID))
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

// unmatched is RouterMiddleware's not-found for a topic no handler serves, marked so IsUnmatchedTopic
// can tell it from a handler's own not-found ("no such order") and from a version nobody serves.
func unmatched(message string) Result[any] {
	result := NotFound[any](message)
	result.unmatched = true
	return result
}

// IsUnmatchedTopic reports whether result is RouterMiddleware's answer to a topic that no handler
// serves at any version. It is a not-found failure like any other, and a one-way binding redelivers
// it by default; this is what lets a binding offer to acknowledge it instead, for a subscriber on a
// shared bus that sees other services' topics (transport-bindings.md §1, obligation 8). A handler's
// own not-found, a version the topic does not serve, and a result a later middleware replaced are
// not unmatched.
func IsUnmatchedTopic(result ResultInfo) bool {
	r, ok := result.(Result[any])
	return ok && r.unmatched
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
