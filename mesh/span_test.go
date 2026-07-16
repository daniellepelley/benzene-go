package mesh

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestSpanFromContext(t *testing.T) {
	t.Run("absent without trace middleware - callers degrade, never fail", func(t *testing.T) {
		if span, ok := SpanFromContext(context.Background()); ok {
			t.Errorf("SpanFromContext() = (%+v, true), want ok=false on a bare context", span)
		}
	})

	t.Run("carries the traced invocation's span to the handler", func(t *testing.T) {
		incomingTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
		var seen Span
		var seenOK bool
		registry := benzene.NewRegistry()
		if err := benzene.Register(registry, benzene.NewTopic("order:create"),
			benzene.Handler[echoRequest, echoResponse](func(ctx context.Context, req echoRequest) benzene.Result[echoResponse] {
				seen, seenOK = SpanFromContext(ctx)
				return benzene.Ok(echoResponse{Text: req.Text})
			})); err != nil {
			t.Fatalf("Register() error = %v", err)
		}

		event, _ := runTraced(t, registry, benzene.NewTopic("order:create"), map[string]string{
			"traceparent": "00-" + incomingTrace + "-00f067aa0ba902b7-01",
		})

		if !seenOK {
			t.Fatal("SpanFromContext() ok = false inside a traced invocation")
		}
		if seen.TraceID != incomingTrace || seen.SpanID != event.SpanID {
			t.Errorf("span = %+v, want trace %q span %q (the ids of the exported event)", seen, incomingTrace, event.SpanID)
		}
	})
}

func TestSpan_Traceparent(t *testing.T) {
	span := Span{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7"}

	got := span.Traceparent()

	want := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if got != want {
		t.Errorf("Traceparent() = %q, want %q", got, want)
	}
	// Round-trip: what one service emits, another service's TraceMiddleware must accept.
	traceID, parentSpanID := parseTraceparent(got)
	if traceID != span.TraceID || parentSpanID != span.SpanID {
		t.Errorf("parseTraceparent(Traceparent()) = (%q, %q), want the original ids", traceID, parentSpanID)
	}
}
