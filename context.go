package benzene

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
