package diagnostics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// capturingSender records the headers it was called with and returns a fixed result.
type capturingSender struct {
	gotHeaders map[string]string
}

func (c *capturingSender) Send(_ context.Context, _ benzene.Topic, headers map[string]string, _ []byte) benzene.Result[json.RawMessage] {
	c.gotHeaders = headers
	return benzene.Accepted[json.RawMessage](nil)
}

// ctxWithSpan returns a context carrying an active, recorded OTel span plus that span's trace id.
func ctxWithSpan(t *testing.T) (context.Context, string) {
	t.Helper()
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "outbound")
	t.Cleanup(func() { span.End() })
	return ctx, span.SpanContext().TraceID().String()
}

func TestTraceContextDecorator_InjectsTraceparentFromActiveSpan(t *testing.T) {
	inner := &capturingSender{}
	sender := TraceContextDecorator(inner)

	ctx, traceID := ctxWithSpan(t)
	result := sender.Send(ctx, benzene.NewTopic("order:create"), map[string]string{"x": "y"}, []byte(`{}`))

	tp := inner.gotHeaders["traceparent"]
	if tp == "" {
		t.Fatalf("no traceparent injected; headers = %v", inner.gotHeaders)
	}
	if !strings.Contains(tp, traceID) {
		t.Errorf("traceparent %q does not carry the active span's trace id %q", tp, traceID)
	}
	if inner.gotHeaders["x"] != "y" {
		t.Errorf("existing header dropped: %v", inner.gotHeaders)
	}
	if !result.Status.IsSuccess() {
		t.Errorf("result Status = %q, want the inner sender's success passed through", result.Status)
	}
}

func TestTraceContextDecorator_NoSpanContextPassesThrough(t *testing.T) {
	inner := &capturingSender{}
	sender := TraceContextDecorator(inner)

	// No span on the context (no Middleware ran / no SDK span): the propagator injects nothing and
	// the call still goes through untouched.
	sender.Send(context.Background(), benzene.NewTopic("order:create"), map[string]string{"x": "y"}, []byte(`{}`))

	if _, present := inner.gotHeaders["traceparent"]; present {
		t.Errorf("traceparent should be absent with no span context; headers = %v", inner.gotHeaders)
	}
	if inner.gotHeaders["x"] != "y" {
		t.Errorf("headers should pass through unchanged; got %v", inner.gotHeaders)
	}
}

func TestTraceContextDecorator_DoesNotOverwriteExistingTraceparent(t *testing.T) {
	for _, key := range []string{"traceparent", "TraceParent"} {
		t.Run(key, func(t *testing.T) {
			inner := &capturingSender{}
			sender := TraceContextDecorator(inner)

			ctx, _ := ctxWithSpan(t)
			existing := "00-abcdefabcdefabcdefabcdefabcdefab-abcdefabcdefabcd-01"

			sender.Send(ctx, benzene.NewTopic("order:create"), map[string]string{key: existing}, []byte(`{}`))

			if got := inner.gotHeaders[key]; got != existing {
				t.Errorf("a caller-set %q was overwritten: %q, want %q", key, got, existing)
			}
		})
	}
}

func TestTraceContextDecorator_DoesNotMutateCallerHeaders(t *testing.T) {
	inner := &capturingSender{}
	sender := TraceContextDecorator(inner)

	ctx, _ := ctxWithSpan(t)
	original := map[string]string{"x": "y"}
	sender.Send(ctx, benzene.NewTopic("order:create"), original, []byte(`{}`))

	if _, present := original["traceparent"]; present {
		t.Errorf("caller's headers map was mutated: %v", original)
	}
}

// Compile-time proof the decorator returns a client.Sender that composes with the other decorators.
var _ client.Sender = TraceContextDecorator(client.SenderFunc(func(context.Context, benzene.Topic, map[string]string, []byte) benzene.Result[json.RawMessage] {
	return benzene.Accepted[json.RawMessage](nil)
}))
