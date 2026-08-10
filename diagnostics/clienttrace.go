package diagnostics

import (
	"context"
	"encoding/json"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"go.opentelemetry.io/otel/propagation"
)

// WithTraceContext wraps next so an outbound Send carries the current OpenTelemetry span
// context as W3C `traceparent` (and `tracestate`) headers - the outbound counterpart to this
// package's inbound Middleware, and the OTel-path sibling of mesh.WithTraceContext. It is
// transport-bindings.md §2's "trace context" client behavior for services observed with OTel
// rather than the zero-dependency mesh trace feed: the module's Middleware already joins an
// inbound traceparent and puts the span on the context, and this closes the loop the Middleware
// doc points at ("outbound clients can propagate it") so a downstream Benzene service continues
// the same trace.
//
// It injects with the same propagation.TraceContext the Middleware reads with, so the two agree on
// header names and format. Degradation matches the rest of the port: with no active/valid span
// context on ctx (no SDK installed, or outside any span) the propagator writes nothing and the
// call is otherwise untouched.
//
// When an active span context IS present it always wins: any W3C trace-context header
// (traceparent/tracestate) already in the outbound headers is replaced (matched case-
// insensitively), matching propagation.TraceContext.Inject's own overwrite semantics. This is
// deliberately UNLIKE WithCorrelationID - a traceparent must re-parent at every hop, so a
// handler that forwards its inbound headers (carrying the inbound traceparent) must not leave the
// downstream call parented to this service's caller. Compose it over any transport's outbound
// client alongside client.WithCorrelationID / client.WithRetry.
func WithTraceContext(next client.Sender) client.Sender {
	propagator := propagation.TraceContext{}
	return client.SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return next.Send(ctx, topic, injectTraceContext(propagator, ctx, headers), message)
	})
}

// injectTraceContext returns headers with the current OTel span context's trace-context headers
// set (replacing any already present, since the active span context owns them), or headers
// unchanged when there is no span context to propagate.
func injectTraceContext(propagator propagation.TraceContext, ctx context.Context, headers map[string]string) map[string]string {
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	if len(carrier) == 0 {
		return headers // no active span context - degrade, inject nothing
	}
	out := make(map[string]string, len(headers)+len(carrier))
	for name, value := range headers {
		if isTraceContextHeader(name) {
			continue // drop any existing (possibly forwarded-inbound) trace-context header
		}
		out[name] = value
	}
	for name, value := range carrier {
		out[name] = value
	}
	return out
}

// isTraceContextHeader reports whether name is a W3C trace-context header (case-insensitive) - the
// headers the active span context owns and this decorator replaces.
func isTraceContextHeader(name string) bool {
	return strings.EqualFold(name, "traceparent") || strings.EqualFold(name, "tracestate")
}
