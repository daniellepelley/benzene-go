// Package diagnostics is the OpenTelemetry-based diagnostics middleware - the Go counterpart
// of the main repo's Benzene.Diagnostics, and the standards-based complement to the mesh's
// own semantic trace feed (mesh.TraceMiddleware): where the mesh feed reports to a Benzene
// collector, this middleware emits ordinary OpenTelemetry spans and metrics that flow to
// whatever OTLP-speaking backend the application already runs (Jaeger, Tempo, Datadog,
// X-Ray, Application Insights - the vendor-neutral answer ROADMAP.md promises instead of
// per-vendor packages).
//
// It depends on the OpenTelemetry *API* only - go.opentelemetry.io/otel and friends - never
// the SDK: the application owns the SDK setup (exporter, sampler, resource) and this
// middleware discovers it through the ambient global providers, or explicitly via
// WithTracerProvider/WithMeterProvider. With no SDK installed the API's no-op defaults make
// the middleware free and silent - the same "a missing feed reduces, never breaks" posture
// as every other optional facility here. That third-party dependency is also why this
// package lives in its own Go module (see RELEASING.md): the root module stays
// zero-dependency.
//
// Register it outermost (before healthcheck/mesh/router interception) so it observes every
// invocation. Both trace feeds compose: mesh.TraceMiddleware and this middleware read the
// same inbound W3C traceparent header, so mesh traces and OTel traces share trace ids.
package diagnostics

import (
	"context"
	"strings"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// scopeName identifies this instrumentation to the providers - the conventional
// import-path-as-scope-name.
const scopeName = "github.com/daniellepelley/benzene-go/diagnostics"

// Option configures Middleware.
type Option func(*options)

type options struct {
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
}

// WithTracerProvider uses tp instead of the ambient global otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) { o.tracerProvider = tp }
}

// WithMeterProvider uses mp instead of the ambient global otel.GetMeterProvider().
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *options) { o.meterProvider = mp }
}

// Middleware returns a benzene.Middleware producing one OpenTelemetry span per invocation -
// named by the topic, SpanKind server, joined to the caller's trace via the inbound W3C
// traceparent header - plus two metrics: the "benzene.messages.processed" counter and the
// "benzene.message.duration" histogram (milliseconds), both attributed by topic, transport,
// and result. Instrument names, span/metric attribute keys, and the result-collapse rule
// (successes collapse to "success"; failures are itemized by their status; a pipeline error
// is "exception") all follow the cross-port standard in the main repo's
// docs/guides/observability-conventions.md - read that first before changing any of the
// literal strings below, since they are read back by name by shipped mesh trace/usage
// sources in other ports.
//
// The span carries the same semantic identity as a mesh.TraceEvent: benzene.topic,
// benzene.version (when versioned), and benzene.status - the Benzene status verbatim, or
// "exception" when a pipeline error escaped past this middleware's position, not an HTTP
// code. (The mesh trace feed, mesh.TraceEvent, makes a different, deliberate choice for that
// same case - mesh.md §3 reports it as an empty status, a wiring gap, with the escaped error
// otherwise invisible to that feed. The two feeds serve different readers and are not
// required to agree.) A non-success status (or a pipeline-level Go error, which
// envelope.Dispatch would map to ServiceUnavailable) marks the span's OTel status as Error.
//
// The transport metric/span attribute is always "<missing>" today: no transport binding in
// this port yet records its identity anywhere this middleware can read back (there is no
// Go equivalent yet of .NET's ICurrentTransport). That is a known gap in the convention's
// Go conformance, not a bug in this middleware; tracked in the main repo's
// docs/guides/observability-conventions.md §5-§6.
//
// The span's context is passed to next, so downstream middleware and handlers see the
// current span (trace.SpanFromContext) and outbound clients can propagate it.
func Middleware(opts ...Option) benzene.Middleware {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.tracerProvider == nil {
		o.tracerProvider = otel.GetTracerProvider()
	}
	if o.meterProvider == nil {
		o.meterProvider = otel.GetMeterProvider()
	}

	tracer := o.tracerProvider.Tracer(scopeName)
	meter := o.meterProvider.Meter(scopeName)
	propagator := propagation.TraceContext{}

	// A provider that cannot build an instrument (the API contract allows it) reduces the
	// metric feed to off; it must never take the span feed - let alone the invocation - down
	// with it.
	messagesProcessed, err := meter.Int64Counter("benzene.messages.processed",
		metric.WithDescription("Messages processed, by topic, transport, and result."))
	if err != nil {
		messagesProcessed = nil
	}
	duration, err := meter.Float64Histogram("benzene.message.duration",
		metric.WithUnit("ms"),
		metric.WithDescription("Message handling duration in milliseconds, by topic, transport, and result."))
	if err != nil {
		duration = nil
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		parent := propagator.Extract(ctx, headerCarrier(ic.Headers))

		attrs := []attribute.KeyValue{attribute.String("benzene.topic", ic.Topic.ID)}
		if ic.Topic.Version != "" {
			attrs = append(attrs, attribute.String("benzene.version", ic.Topic.Version))
		}

		spanCtx, span := tracer.Start(parent, ic.Topic.ID,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attrs...))

		started := time.Now()
		err := next(spanCtx)
		elapsedMs := float64(time.Since(started)) / float64(time.Millisecond)

		status := ""
		if ic.Result != nil {
			status = string(ic.Result.ResultStatus())
		}
		if err != nil {
			// A pipeline error escaping past this middleware's position never produced (or
			// overrides) a Result-derived status - its own category, matching .NET's
			// ActivityMiddlewareDecorator and the observability-conventions.md standard.
			status = "exception"
		}
		statusAttr := attribute.String("benzene.status", status)
		span.SetAttributes(statusAttr)

		switch {
		case err != nil:
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		case status != "" && benzene.Status(status).IsFailure():
			span.SetStatus(codes.Error, strings.Join(ic.Result.ResultErrors(), ", "))
		}
		span.End()

		// The metrics "result" attribute is deliberately not the raw status: successes
		// collapse to "success" (cardinality budget - the total matters, not Ok vs Created)
		// while failures are itemized by status, per observability-conventions.md §3.
		result := "<missing>"
		switch {
		case err != nil:
			result = "exception"
		case ic.Result != nil:
			switch {
			case resultSuccessful(ic.Result):
				result = "success"
			case string(ic.Result.ResultStatus()) != "":
				result = string(ic.Result.ResultStatus())
			default:
				result = "failure"
			}
		}
		metricAttrs := metric.WithAttributes(
			attribute.String("topic", ic.Topic.ID),
			attribute.String("transport", "<missing>"),
			attribute.String("result", result),
		)
		if messagesProcessed != nil {
			messagesProcessed.Add(ctx, 1, metricAttrs)
		}
		if duration != nil {
			duration.Record(ctx, elapsedMs, metricAttrs)
		}

		return err
	}
}

// resultSuccessful reads the result's success flag off the type-erased ResultInfo (via the
// optional ResultIsSuccessful interface every Result[T] satisfies), falling back to the
// status class if some other ResultInfo implementation does not expose it - the same idiom
// as envelope.resultSuccessful (and mesh/idempotency/resilience/circuitbreaker's local
// copies); each package keeps its own to avoid a shared-package dependency for one helper.
func resultSuccessful(result benzene.ResultInfo) bool {
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		return s.ResultIsSuccessful()
	}
	return !result.ResultStatus().IsFailure()
}

// headerCarrier adapts the flat wire header map to OpenTelemetry's TextMapCarrier, so the
// W3C propagator reads traceparent/tracestate exactly where every binding puts them. Reads
// are direct: wire-contracts.md §2 headers are already lower-cased by the bindings, and
// the propagator asks for lower-case keys.
type headerCarrier map[string]string

func (c headerCarrier) Get(key string) string { return c[key] }

func (c headerCarrier) Set(key, value string) { c[key] = value }

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
