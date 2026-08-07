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

// TraceContextDecorator wraps next so an outbound Send propagates the current invocation's mesh
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
// alongside client.CorrelationDecorator / client.RetryDecorator; httpclient.Client and every
// binding's Client satisfy client.Sender.
//
// Degradation is the package rule: with no trace middleware installed (no span on the context) the
// decorator sends no traceparent and the call is otherwise untouched - an unmeshed hop loses trace
// continuity, never the request. An existing `traceparent` in the outbound headers is left as-is
// (matched case-insensitively and never overwritten), mirroring CorrelationDecorator, so a caller
// that set one deliberately keeps control.
func TraceContextDecorator(next client.Sender) client.Sender {
	return client.SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return next.Send(ctx, topic, withTraceparent(ctx, headers), message)
	})
}

// withTraceparent returns headers with the current span's traceparent added, or headers unchanged
// when there is no span (degrade) or one is already present (never overwrite).
func withTraceparent(ctx context.Context, headers map[string]string) map[string]string {
	span, ok := SpanFromContext(ctx)
	if !ok {
		return headers // no trace context - degrade, don't fabricate one
	}
	for name := range headers {
		if strings.EqualFold(name, headerTraceparent) {
			return headers // already set - never overwrite
		}
	}
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out[headerTraceparent] = span.Traceparent()
	return out
}
