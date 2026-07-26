package benzene

import (
	"context"
	"strings"
)

// InvocationContext carries the state of a single pipeline invocation (core-concepts.md
// §6): the resolved topic, headers, the native/raw request payload (converted to a
// handler's declared TReq by the terminal router middleware - see convertRequest), and a
// slot for the result once a handler has run.
//
// Cancellation, deadlines, and other invocation-scoped facts ride on the ctx.Context
// parameter threaded through Pipeline.Run and every Middleware call, per core-concepts.md
// §4 ("the pipeline carries no cancellation parameter... rides on the context") - Go's
// context.Context already carries that here, so nothing extra is needed on
// InvocationContext itself for that concern.
type InvocationContext struct {
	Topic   Topic
	Headers map[string]string
	// Request is the native/raw request payload for this invocation (e.g. a JSON body as
	// []byte, or an already-typed value for zero-copy passthrough). The router middleware
	// converts it into the resolved handler's declared TReq.
	Request any
	// Result is populated by the router middleware once the handler (or the NotFound /
	// error fallback) has run. Middleware registered after the router can inspect or
	// replace it; middleware registered before the router runs before it exists.
	Result ResultInfo
	// Scope is this invocation's per-invocation DI scope (scope.go).
	Scope *Scope
	// ResponseHeaders holds outbound transport headers set during this invocation - by
	// middleware directly, or by a handler via SetResponseHeader(ctx, ...) (the router puts
	// this invocation context on the handler's ctx, the same accessor pattern as
	// ScopeFromContext). A binding merges these onto its response after dispatch: the wire
	// envelope's headers for envelope-shaped transports, real response headers for the native
	// HTTP binding. Nil until the first set - fire-and-forget transports never allocate it.
	ResponseHeaders map[string]string
}

// SetResponseHeader records an outbound header on this invocation, to be merged onto the
// transport response by the binding. Names are lower-cased, matching wire-contracts.md §2's
// "SHOULD be written lower-case" and the inbound flattening every binding already does; a
// repeated name overwrites (last write wins).
func (ic *InvocationContext) SetResponseHeader(name, value string) {
	if ic.ResponseHeaders == nil {
		ic.ResponseHeaders = map[string]string{}
	}
	ic.ResponseHeaders[strings.ToLower(name)] = value
}

// invocationContextKey is an unexported type so contextWithInvocation's value can't collide
// with a key some other package puts on the same context.Context.
type invocationContextKey struct{}

// contextWithInvocation returns a copy of ctx carrying ic - RouterMiddleware calls this
// before invoking a handler, the same accessor pattern (core-concepts.md §4) as
// ContextWithScope, so handler-facing helpers like the package-level SetResponseHeader can
// reach the invocation without widening the Handler signature.
func contextWithInvocation(ctx context.Context, ic *InvocationContext) context.Context {
	return context.WithValue(ctx, invocationContextKey{}, ic)
}

// SetResponseHeader records an outbound transport header for the invocation ctx belongs to -
// the handler-side counterpart of InvocationContext.SetResponseHeader, for handlers (whose
// signature carries no *InvocationContext). ok = false if ctx carries no invocation (e.g. in
// a unit test that calls a handler directly) - the header is then dropped, matching how a
// handler must keep working when a transport has nowhere to put response headers anyway.
func SetResponseHeader(ctx context.Context, name, value string) (ok bool) {
	ic, ok := ctx.Value(invocationContextKey{}).(*InvocationContext)
	if !ok {
		return false
	}
	ic.SetResponseHeader(name, value)
	return true
}

// NewInvocationContext builds an InvocationContext for one pipeline invocation. headers may
// be nil, in which case an empty map is used.
func NewInvocationContext(topic Topic, headers map[string]string, request any, scope *Scope) *InvocationContext {
	if headers == nil {
		headers = map[string]string{}
	}
	return &InvocationContext{
		Topic:   topic,
		Headers: headers,
		Request: request,
		Scope:   scope,
	}
}
