package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// captureExporter records every exported event, concurrency-safe like a real exporter.
type captureExporter struct {
	mu     sync.Mutex
	events []TraceEvent
}

func (c *captureExporter) Export(_ context.Context, event TraceEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *captureExporter) single(t *testing.T) TraceEvent {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) != 1 {
		t.Fatalf("exported %d events, want 1: %v", len(c.events), c.events)
	}
	return c.events[0]
}

func panicHandler(_ context.Context, _ echoRequest) benzene.Result[echoResponse] {
	panic("boom")
}

func runTraced(t *testing.T, registry *benzene.Registry, topic benzene.Topic, headers map[string]string) (TraceEvent, *benzene.InvocationContext) {
	t.Helper()
	exporter := &captureExporter{}
	info := ServiceInfo{Service: "orders", InstanceID: "orders-1"}
	pipeline := benzene.NewPipeline(TraceMiddleware(info, exporter), benzene.RouterMiddleware(registry))
	ic := benzene.NewInvocationContext(topic, headers, []byte(`{"text":"hi"}`), nil)

	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return exporter.single(t), ic
}

func TestTraceMiddleware_RecordsSemanticInvocations(t *testing.T) {
	tests := []struct {
		name       string
		topic      benzene.Topic
		register   bool
		handler    benzene.Handler[echoRequest, echoResponse]
		wantStatus string
	}{
		{
			name:       "successful invocation",
			topic:      benzene.NewTopic("order:create").WithVersion("v2"),
			register:   true,
			handler:    echoHandler,
			wantStatus: "Ok",
		},
		{
			name:       "missing handler becomes NotFound, still traced",
			topic:      benzene.NewTopic("no:such:topic"),
			register:   false,
			wantStatus: "NotFound",
		},
		{
			name:       "handler panic becomes ServiceUnavailable, still traced",
			topic:      benzene.NewTopic("order:create"),
			register:   true,
			handler:    panicHandler,
			wantStatus: "ServiceUnavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := benzene.NewRegistry()
			if tt.register {
				if err := benzene.Register(registry, tt.topic, tt.handler); err != nil {
					t.Fatalf("Register() error = %v", err)
				}
			}

			event, _ := runTraced(t, registry, tt.topic, map[string]string{"x-correlation-id": "corr-1"})

			if event.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", event.Status, tt.wantStatus)
			}
			if event.Topic != tt.topic.ID || event.TopicVersion != tt.topic.Version {
				t.Errorf("Topic = %q@%q, want %q@%q", event.Topic, event.TopicVersion, tt.topic.ID, tt.topic.Version)
			}
			if event.Service != "orders" || event.InstanceID != "orders-1" {
				t.Errorf("identity = %q/%q, want orders/orders-1", event.Service, event.InstanceID)
			}
			if event.CorrelationID != "corr-1" {
				t.Errorf("CorrelationID = %q, want %q", event.CorrelationID, "corr-1")
			}
			if len(event.TraceID) != 32 || len(event.SpanID) != 16 {
				t.Errorf("ids = %q/%q, want 32/16 hex chars", event.TraceID, event.SpanID)
			}
			if event.ParentSpanID != "" {
				t.Errorf("ParentSpanID = %q, want empty without an incoming traceparent", event.ParentSpanID)
			}
			if event.DurationMs < 0 {
				t.Errorf("DurationMs = %v, want >= 0", event.DurationMs)
			}
			if event.StartedAt.IsZero() {
				t.Error("StartedAt is zero")
			}
		})
	}
}

func TestTraceMiddleware_JoinsIncomingTraceparent(t *testing.T) {
	incomingTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
	incomingParent := "00f067aa0ba902b7"
	registry := newTestRegistry(t, benzene.NewTopic("order:create"))

	event, _ := runTraced(t, registry, benzene.NewTopic("order:create"), map[string]string{
		"traceparent": "00-" + incomingTrace + "-" + incomingParent + "-01",
	})

	if event.TraceID != incomingTrace {
		t.Errorf("TraceID = %q, want the incoming trace id %q", event.TraceID, incomingTrace)
	}
	if event.ParentSpanID != incomingParent {
		t.Errorf("ParentSpanID = %q, want %q", event.ParentSpanID, incomingParent)
	}
	if event.SpanID == incomingParent || len(event.SpanID) != 16 {
		t.Errorf("SpanID = %q, want a fresh 16-hex-char id distinct from the parent", event.SpanID)
	}
}

func TestParseTraceparent_RejectsMalformedValues(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{name: "empty", header: ""},
		{name: "wrong segment count", header: "00-abc-def"},
		{name: "version wrong length", header: "0-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "trace id wrong length", header: "00-4bf92f35-00f067aa0ba902b7-01"},
		{name: "parent id wrong length", header: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa-01"},
		{name: "flags wrong length", header: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-011"},
		{name: "non-hex version", header: "zz-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "non-hex trace id", header: "00-zzf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "non-hex parent id", header: "00-4bf92f3577b34da6a3ce929d0e0e4736-zzf067aa0ba902b7-01"},
		{name: "non-hex flags", header: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-zz"},
		{name: "all-zero trace id is invalid per spec", header: "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
		{name: "all-zero parent id is invalid per spec", header: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traceID, parentSpanID := parseTraceparent(tt.header)

			if traceID != "" || parentSpanID != "" {
				t.Errorf("parseTraceparent(%q) = (%q, %q), want empty", tt.header, traceID, parentSpanID)
			}
		})
	}
}

func TestTraceMiddleware_DegradesNeverTheService(t *testing.T) {
	t.Run("nil exporter is a pass-through", func(t *testing.T) {
		registry := newTestRegistry(t, benzene.NewTopic("order:create"))
		pipeline := benzene.NewPipeline(TraceMiddleware(ServiceInfo{}, nil), benzene.RouterMiddleware(registry))
		ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), nil, []byte(`{"text":"hi"}`), nil)

		if err := pipeline.Run(context.Background(), ic); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusOk {
			t.Errorf("Result = %v, want Ok - the service must behave exactly as unmeshed", ic.Result)
		}
	})

	t.Run("panicking exporter loses its event, not the response", func(t *testing.T) {
		registry := newTestRegistry(t, benzene.NewTopic("order:create"))
		exporter := panickingExporter{}
		pipeline := benzene.NewPipeline(TraceMiddleware(ServiceInfo{}, exporter), benzene.RouterMiddleware(registry))
		ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), nil, []byte(`{"text":"hi"}`), nil)

		if err := pipeline.Run(context.Background(), ic); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusOk {
			t.Errorf("Result = %v, want Ok despite the exporter panicking", ic.Result)
		}
	})

	t.Run("a downstream pipeline error is exported and propagated", func(t *testing.T) {
		wantErr := errors.New("transport-level failure")
		exporter := &captureExporter{}
		pipeline := benzene.NewPipeline(
			TraceMiddleware(ServiceInfo{}, exporter),
			func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
				return wantErr
			},
		)
		ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), nil, nil, nil)

		if err := pipeline.Run(context.Background(), ic); !errors.Is(err, wantErr) {
			t.Fatalf("Run() error = %v, want %v", err, wantErr)
		}

		event := exporter.single(t)
		if event.Status != "" {
			t.Errorf("Status = %q, want empty when no Result was produced", event.Status)
		}
		if event.Topic != "order:create" {
			t.Errorf("Topic = %q, want %q", event.Topic, "order:create")
		}
	})
}

type panickingExporter struct{}

func (panickingExporter) Export(context.Context, TraceEvent) { panic("exporter down") }

func TestTraceEvent_WireFieldNamesAreCamelCase(t *testing.T) {
	registry := newTestRegistry(t, benzene.NewTopic("order:create").WithVersion("v2"))
	event, _ := runTraced(t, registry, benzene.NewTopic("order:create").WithVersion("v2"), map[string]string{
		"traceparent":      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"x-correlation-id": "corr-1",
	})

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	for _, key := range []string{"traceId", "spanId", "parentSpanId", "service", "instanceId", "topic", "topicVersion", "status", "durationMs", "startedAt", "correlationId"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("marshaled event is missing key %q: %s", key, data)
		}
	}
}

func TestRandHex(t *testing.T) {
	a, b := randHex(16), randHex(16)

	if len(a) != 32 || len(b) != 32 {
		t.Errorf("randHex(16) lengths = %d/%d, want 32", len(a), len(b))
	}
	if a == b {
		t.Errorf("randHex(16) returned the same value twice: %q", a)
	}
	if strings.ToLower(a) != a {
		t.Errorf("randHex(16) = %q, want lowercase hex", a)
	}
	if got := randHex(8); len(got) != 16 {
		t.Errorf("randHex(8) length = %d, want 16", len(got))
	}
}
