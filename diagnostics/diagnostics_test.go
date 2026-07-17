package diagnostics

import (
	"context"
	"errors"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

// newTestPipeline wires the middleware under test (with SDK-backed in-memory providers)
// outermost, ahead of the router - the documented registration order.
func newTestPipeline(t *testing.T, opts ...Option) *benzene.Pipeline {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return benzene.NewPipeline(Middleware(opts...), benzene.RouterMiddleware(registry))
}

func run(t *testing.T, pipeline *benzene.Pipeline, headers map[string]string, request any) *benzene.InvocationContext {
	t.Helper()
	ic := benzene.NewInvocationContext(benzene.NewTopic("greet"), headers, request, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return ic
}

func attrValue(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func TestMiddleware_SuccessfulInvocationProducesServerSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	pipeline := newTestPipeline(t, WithTracerProvider(tp))
	run(t, pipeline, nil, greetRequest{Name: "World"})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != "greet" {
		t.Errorf("span name = %q, want %q", span.Name, "greet")
	}
	if span.SpanKind != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", span.SpanKind)
	}
	if got, ok := attrValue(span.Attributes, "benzene.topic"); !ok || got != "greet" {
		t.Errorf(`attribute "benzene.topic" = %q (present=%v), want "greet"`, got, ok)
	}
	if got, ok := attrValue(span.Attributes, "benzene.status"); !ok || got != string(benzene.StatusOk) {
		t.Errorf(`attribute "benzene.status" = %q (present=%v), want %q`, got, ok, benzene.StatusOk)
	}
	if span.Status.Code == codes.Error {
		t.Errorf("span status = Error, want not-Error for a success result; description = %q", span.Status.Description)
	}
}

func TestMiddleware_FailureResultMarksSpanError(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	pipeline := newTestPipeline(t, WithTracerProvider(tp))
	run(t, pipeline, nil, greetRequest{Name: ""})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.Status.Code != codes.Error {
		t.Errorf("span status code = %v, want Error for a non-success result", span.Status.Code)
	}
	if !strings.Contains(span.Status.Description, "name is required") {
		t.Errorf("span status description = %q, want the result's error detail", span.Status.Description)
	}
	if got, _ := attrValue(span.Attributes, "benzene.status"); got != string(benzene.StatusBadRequest) {
		t.Errorf(`attribute "benzene.status" = %q, want %q`, got, benzene.StatusBadRequest)
	}
}

func TestMiddleware_PipelineErrorMarksSpanErrorAndPropagates(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	boom := errors.New("middleware exploded")
	failing := benzene.Middleware(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		return boom
	})
	pipeline := benzene.NewPipeline(Middleware(WithTracerProvider(tp)), failing)

	ic := benzene.NewInvocationContext(benzene.NewTopic("greet"), nil, nil, nil)
	if err := pipeline.Run(context.Background(), ic); !errors.Is(err, boom) {
		t.Fatalf("Run() error = %v, want the propagated middleware error", err)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Errorf("span status code = %v, want Error for a pipeline error", spans[0].Status.Code)
	}
	if len(spans[0].Events) == 0 {
		t.Error("span has no events, want the recorded error event")
	}
}

func TestMiddleware_JoinsInboundTraceparent(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	pipeline := newTestPipeline(t, WithTracerProvider(tp))
	headers := map[string]string{"traceparent": "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"}
	run(t, pipeline, headers, greetRequest{Name: "World"})

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	span := spans[0]
	if got := span.SpanContext.TraceID().String(); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("trace id = %q, want the inbound traceparent's trace id", got)
	}
	if got := span.Parent.SpanID().String(); got != "0123456789abcdef" {
		t.Errorf("parent span id = %q, want the inbound traceparent's parent id", got)
	}
}

func TestMiddleware_VersionedTopicCarriesVersionAttribute(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	pipeline := benzene.NewPipeline(Middleware(WithTracerProvider(tp)))
	ic := benzene.NewInvocationContext(benzene.NewTopic("greet").WithVersion("2"), nil, nil, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	if got, ok := attrValue(spans[0].Attributes, "benzene.topic.version"); !ok || got != "2" {
		t.Errorf(`attribute "benzene.topic.version" = %q (present=%v), want "2"`, got, ok)
	}
	// No downstream middleware produced a Result - the status attribute reports that
	// verbatim as empty rather than papering over it, matching the mesh feed.
	if got, ok := attrValue(spans[0].Attributes, "benzene.status"); !ok || got != "" {
		t.Errorf(`attribute "benzene.status" = %q (present=%v), want ""`, got, ok)
	}
}

func TestMiddleware_RecordsMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	pipeline := newTestPipeline(t, WithMeterProvider(mp))
	run(t, pipeline, nil, greetRequest{Name: "World"})
	run(t, pipeline, nil, greetRequest{Name: ""})

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(collected.ScopeMetrics) != 1 {
		t.Fatalf("collected %d scopes, want 1", len(collected.ScopeMetrics))
	}
	scope := collected.ScopeMetrics[0]
	if scope.Scope.Name != scopeName {
		t.Errorf("scope name = %q, want %q", scope.Scope.Name, scopeName)
	}

	byName := map[string]metricdata.Metrics{}
	for _, m := range scope.Metrics {
		byName[m.Name] = m
	}

	counter, ok := byName["benzene.invocations"].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf(`"benzene.invocations" data = %T, want Sum[int64]`, byName["benzene.invocations"].Data)
	}
	var total int64
	statuses := map[string]bool{}
	for _, point := range counter.DataPoints {
		total += point.Value
		if status, found := point.Attributes.Value(attribute.Key("benzene.status")); found {
			statuses[status.AsString()] = true
		}
	}
	if total != 2 {
		t.Errorf("invocation count = %d, want 2", total)
	}
	if !statuses[string(benzene.StatusOk)] || !statuses[string(benzene.StatusBadRequest)] {
		t.Errorf("counter statuses = %v, want both Ok and BadRequest series", statuses)
	}

	histogram, ok := byName["benzene.invocation.duration"].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf(`"benzene.invocation.duration" data = %T, want Histogram[float64]`, byName["benzene.invocation.duration"].Data)
	}
	var samples uint64
	for _, point := range histogram.DataPoints {
		samples += point.Count
	}
	if samples != 2 {
		t.Errorf("duration samples = %d, want 2", samples)
	}
}

// failingMeter refuses to build the two instruments - the API contract allows a provider to
// error there, and the middleware must reduce the metric feed to off, never the invocation.
type failingMeter struct {
	noop.Meter
}

func (failingMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, errors.New("counter refused")
}

func (failingMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return nil, errors.New("histogram refused")
}

type failingMeterProvider struct {
	noop.MeterProvider
}

func (failingMeterProvider) Meter(string, ...metric.MeterOption) metric.Meter {
	return failingMeter{}
}

func TestMiddleware_InstrumentFailureReducesMetricsNeverTheInvocation(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	pipeline := newTestPipeline(t, WithTracerProvider(tp), WithMeterProvider(failingMeterProvider{}))
	ic := run(t, pipeline, nil, greetRequest{Name: "World"})

	if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("Result = %v, want Ok - a failed instrument must not affect the invocation", ic.Result)
	}
	if len(exporter.GetSpans()) != 1 {
		t.Errorf("exported %d spans, want 1 - the span feed must survive a failed metric feed", len(exporter.GetSpans()))
	}
}

func TestMiddleware_NoSDKIsSilentPassThrough(t *testing.T) {
	// No providers configured and no global SDK installed: the API's no-op defaults apply,
	// and the invocation must behave exactly as without the middleware.
	pipeline := newTestPipeline(t)
	ic := run(t, pipeline, nil, greetRequest{Name: "World"})

	if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("Result = %v, want Ok - the no-op providers must not affect the invocation", ic.Result)
	}
}

func TestHeaderCarrier(t *testing.T) {
	carrier := headerCarrier{"traceparent": "abc"}

	if got := carrier.Get("traceparent"); got != "abc" {
		t.Errorf("Get() = %q, want %q", got, "abc")
	}
	carrier.Set("tracestate", "vendor=1")
	if got := carrier.Get("tracestate"); got != "vendor=1" {
		t.Errorf("Get() after Set() = %q, want %q", got, "vendor=1")
	}
	keys := carrier.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys() = %v, want 2 keys", keys)
	}
}
