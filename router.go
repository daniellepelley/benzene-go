package benzene

import (
	"context"
	"fmt"
)

// RouterMiddleware returns the terminal middleware that resolves ic.Topic against registry
// and dispatches to the matching handler, storing the outcome on ic.Result. Conventionally
// registered last in a Pipeline (core-concepts.md §4).
//
// Per core-concepts.md §2/§5, this middleware never returns a Go error for an application-
// level outcome - a missing topic, a missing handler, a request-conversion failure, or a
// handler panic all become a Result on ic.Result (ValidationError, NotFound, BadRequest, and
// ServiceUnavailable respectively), so every caller uniformly reads ic.Result rather than
// distinguishing "no handler" from "handler ran" via the Go error return. A handler panic
// specifically MUST NOT crash the transport adapter (§5) - recovered here and mapped to
// ServiceUnavailable, which wire-contracts.md §3 defines as "also the mapping for uncaught
// handler exceptions."
func RouterMiddleware(registry *Registry) Middleware {
	return func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) (err error) {
		if ic.Topic.ID == "" {
			ic.Result = ValidationError[any]("topic is missing")
			return next(ctx)
		}

		handler, ok := registry.resolve(ic.Topic)
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
