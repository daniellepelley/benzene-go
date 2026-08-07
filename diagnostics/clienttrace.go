package diagnostics

import (
	"context"
	"encoding/json"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"go.opentelemetry.io/otel/propagation"
)

// TraceContextDecorator wraps next so an outbound Send carries the current OpenTelemetry span
// context as W3C `traceparent` (and `tracestate`) headers - the outbound counterpart to this
// package's inbound Middleware, and the OTel-path sibling of mesh.TraceContextDecorator. It is
// transport-bindings.md §2's "trace context" client behavior for services observed with OTel
// rather than the zero-dependency mesh trace feed: the module's Middleware already joins an
// inbound traceparent and puts the span on the context, and this closes the loop the Middleware
// doc points at ("outbound clients can propagate it") so a downstream Benzene service continues
// the same trace.
//
// It injects with the same propagation.TraceContext the Middleware reads with, so the two agree on
// header names and format. Degradation matches the rest of the port: with no active/valid span
// context on ctx (no SDK installed, or outside any span) the propagator writes nothing and the
// call is otherwise untouched; a header the caller already set is matched case-insensitively and
// never overwritten. Compose it over any transport's outbound client alongside
// client.CorrelationDecorator / client.RetryDecorator.
func TraceContextDecorator(next client.Sender) client.Sender {
	propagator := propagation.TraceContext{}
	return client.SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return next.Send(ctx, topic, injectTraceContext(propagator, ctx, headers), message)
	})
}

// injectTraceContext returns headers with the current OTel span context's trace-context headers
// added, or headers unchanged when there is no span context to propagate. Caller-set headers win:
// the propagator writes into a separate carrier and only non-colliding keys (case-insensitive) are
// merged in, so a traceparent the caller set deliberately is never overwritten.
func injectTraceContext(propagator propagation.TraceContext, ctx context.Context, headers map[string]string) map[string]string {
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	if len(carrier) == 0 {
		return headers // no active span context - degrade, inject nothing
	}
	out := make(map[string]string, len(headers)+len(carrier))
	for name, value := range headers {
		out[name] = value
	}
	for name, value := range carrier {
		if !containsFold(out, name) {
			out[name] = value
		}
	}
	return out
}

// containsFold reports whether m has a key equal to key under Unicode case-folding.
func containsFold(m map[string]string, key string) bool {
	for name := range m {
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}
