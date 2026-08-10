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
const headerTraceparent = "traceparent"

// WithTraceContext wraps next so an outbound Send propagates the current invocation's mesh
// trace as a W3C `traceparent` header. It is the outbound half of trace-bindings.md §2's third
// cross-cutting client behavior ("correlation ID injection, trace context, retry") - the direct
// counterpart to this package's inbound TraceMiddleware, and it lives here rather than in `client`
// because the trace context it propagates is this package's Span (keeping `client` free of any
// mesh dependency).
//
// It reads the span TraceMiddleware recorded for this invocation (SpanFromContext) and, when
// present, sets `traceparent` to that span's value - this invocation as the downstream call's
// parent - which is exactly what lets a collector derive who-calls-whom across services without
// anyone declaring an edge (see Span.Traceparent). Compose it over any transport's outbound client
// alongside client.WithCorrelationID / client.WithRetry; httpclient.Client and every
// binding's Client satisfy client.Sender.
//
// Degradation is the package rule: with no trace middleware installed (no span on the context) the
// decorator sends no traceparent and the call is otherwise untouched - an unmeshed hop loses trace
// continuity, never the request.
//
// When a span IS present it always wins: any `traceparent` already in the outbound headers is
// replaced (matched case-insensitively). This is deliberately UNLIKE WithCorrelationID - a
// correlation id propagates unchanged down a chain, but a traceparent must re-parent at every hop.
// The case that makes this load-bearing: a handler that forwards its own inbound headers onto the
// outbound call carries the *inbound* traceparent; leaving it would parent the downstream call to
// this service's caller and hide this service from the derived who-calls-whom graph - the exact
// thing the decorator exists to get right.
func WithTraceContext(next client.Sender) client.Sender {
	return client.SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return next.Send(ctx, topic, withTraceparent(ctx, headers), message)
	})
}

// withTraceparent returns headers with the current span's traceparent set (replacing any already
// present, since this invocation's span is the correct parent for the outbound hop), or headers
// unchanged when there is no span (degrade).
func withTraceparent(ctx context.Context, headers map[string]string) map[string]string {
	span, ok := SpanFromContext(ctx)
	if !ok {
		return headers // no trace context - degrade, don't fabricate one
	}
	out := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		if strings.EqualFold(name, headerTraceparent) {
			continue // drop any existing (possibly forwarded-inbound) traceparent - this span wins
		}
		out[name] = value
	}
	out[headerTraceparent] = span.Traceparent()
	return out
}
