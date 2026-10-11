package mesh

import (
	"context"
	"encoding/json"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// headerTraceparent is the W3C trace-context header TraceMiddleware reads on the way in and this
// decorator writes on the way out - the one string that carries trace continuity across a hop.
// headerTracestate is its vendor-state companion, carried forward unchanged.
const (
	headerTraceparent = "traceparent"
	headerTracestate  = "tracestate"
)

// WithTraceContext wraps next so an outbound Send propagates the current invocation's mesh trace as
// a W3C `traceparent` header, with the caller's `tracestate` carried forward unchanged - the outbound
// half of the meshed-service claim's M2 (mesh.md §3, §7.1). It covers every outbound message a
// Sender carries: a send with a reply, a one-way send (every one-way binding's Client is a Sender
// too), and a published event (responseevents.NewSenderPublisher publishes through a Sender, so
// wrapping that Sender propagates the trace onto events). It is also the trace-context entry of
// trace-bindings.md §2's cross-cutting client behaviors ("correlation ID injection, trace context,
// retry") - the direct counterpart to this package's inbound TraceMiddleware, and it lives here
// rather than in `client` because the trace context it propagates is this package's Span (keeping
// `client` free of any mesh dependency).
//
// It reads the span TraceMiddleware recorded for this invocation (SpanFromContext) and, when
// present, sets `traceparent` to that span's value - this invocation as the downstream message's
// parent - which is what lets a collector correlate an observed message with the declared edge it
// exercises (mesh.md §4.2); the graph itself comes from the registered Contract Documents, never
// from traces. Compose it over any transport's outbound client alongside client.WithCorrelationID /
// client.WithRetry; httpclient.Client and every binding's Client satisfy client.Sender.
//
// Degradation is the package rule: with no trace middleware installed (no span on the context) the
// decorator sends no traceparent and the call is otherwise untouched - an unmeshed hop loses trace
// continuity, never the request.
//
// When a span IS present it always wins: any `traceparent` (and `tracestate`) already in the
// outbound headers is replaced (matched case-insensitively). This is deliberately UNLIKE
// WithCorrelationID - a correlation id propagates unchanged down a chain, but a traceparent must
// re-parent at every hop. The case that makes this load-bearing: a handler that forwards its own
// inbound headers onto the outbound call carries the *inbound* traceparent; leaving it would parent
// the downstream call to this service's caller and hide this service from the observed path.
func WithTraceContext(next client.Sender) client.Sender {
	return client.SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return next.Send(ctx, topic, withTraceparent(ctx, headers), message)
	})
}

// withTraceparent returns headers with the current span's traceparent (and tracestate, when the
// span carries one) set, replacing any already present, since this invocation's span is the correct
// parent for the outbound hop - or headers unchanged when there is no span (degrade).
func withTraceparent(ctx context.Context, headers map[string]string) map[string]string {
	span, ok := SpanFromContext(ctx)
	if !ok {
		return headers // no trace context - degrade, don't fabricate one
	}
	out := make(map[string]string, len(headers)+2)
	for name, value := range headers {
		if strings.EqualFold(name, headerTraceparent) || strings.EqualFold(name, headerTracestate) {
			continue // drop any existing (possibly forwarded-inbound) trace context - this span wins
		}
		out[name] = value
	}
	out[headerTraceparent] = span.Traceparent()
	if span.TraceState != "" {
		out[headerTracestate] = span.TraceState
	}
	return out
}
