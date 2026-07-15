package benzene

import "context"

// Middleware wraps invocation handling in an ordered onion pipeline (core-concepts.md §4).
// A middleware that does not call next terminates the pipeline; everything after it
// (including the handler dispatch, if registered later) does not run - this is the
// mechanism behind features like health-check interception.
//
// Cancellation/deadlines ride on ctx, not on this signature, so the shape is identical
// across transports that have no cancellation concept at all.
type Middleware func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error

// Pipeline is an ordered list of middleware. The first registered is outermost.
type Pipeline struct {
	middlewares []Middleware
}

// NewPipeline builds a Pipeline from middlewares in registration order. The terminal
// message router (see RouterMiddleware) is an ordinary middleware and, per core-concepts.md
// §4, is conventionally registered last.
func NewPipeline(middlewares ...Middleware) *Pipeline {
	return &Pipeline{middlewares: middlewares}
}

// Run executes the pipeline exactly once for ic. One transport event (one HTTP request,
// one queue message, ...) is exactly one Run call, per core-concepts.md §4 - a batch
// delivery is one Run per message, each with its own InvocationContext/Scope; arranging
// that is the transport binding's responsibility, not Pipeline's.
func (p *Pipeline) Run(ctx context.Context, ic *InvocationContext) error {
	return p.runFrom(ctx, ic, 0)
}

func (p *Pipeline) runFrom(ctx context.Context, ic *InvocationContext, index int) error {
	if index >= len(p.middlewares) {
		return nil
	}
	mw := p.middlewares[index]
	return mw(ctx, ic, func(nextCtx context.Context) error {
		return p.runFrom(nextCtx, ic, index+1)
	})
}
