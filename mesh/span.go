package mesh

import "context"

// Span is the current invocation's position in a trace: the ids TraceMiddleware assigned
// (or adopted from the caller's traceparent) for the event it will export. Handlers use it
// to propagate the trace onto outbound calls, which is what lets a collector join spans
// across services and derive who-calls-whom without anyone declaring it.
type Span struct {
	TraceID string
	SpanID  string
}

// Traceparent renders the span as a W3C traceparent header value for outbound
// propagation: version 00, this span as the parent-id, sampled flag set.
func (s Span) Traceparent() string {
	return "00-" + s.TraceID + "-" + s.SpanID + "-01"
}

type spanContextKey struct{}

func contextWithSpan(ctx context.Context, span Span) context.Context {
	return context.WithValue(ctx, spanContextKey{}, span)
}

// SpanFromContext returns the span TraceMiddleware recorded for the current invocation.
// ok is false when no trace middleware is installed - per this package's degradation
// rule, a caller must then simply send no traceparent header (an unmeshed hop degrades
// trace continuity, never the call).
func SpanFromContext(ctx context.Context) (Span, bool) {
	span, ok := ctx.Value(spanContextKey{}).(Span)
	return span, ok
}
