package mesh

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// capturingSender records the headers it was called with and returns a fixed result, so a test can
// assert on what the decorator forwarded.
type capturingSender struct {
	gotHeaders map[string]string
	result     benzene.Result[json.RawMessage]
}

func (c *capturingSender) Send(_ context.Context, _ benzene.Topic, headers map[string]string, _ []byte) benzene.Result[json.RawMessage] {
	c.gotHeaders = headers
	return c.result
}

func TestTraceContextDecorator_InjectsTraceparentFromSpan(t *testing.T) {
	inner := &capturingSender{result: benzene.Accepted[json.RawMessage](nil)}
	sender := TraceContextDecorator(inner)

	span := Span{TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef"}
	ctx := contextWithSpan(context.Background(), span)

	result := sender.Send(ctx, benzene.NewTopic("order:create"), map[string]string{"x": "y"}, []byte(`{}`))

	if got := inner.gotHeaders[headerTraceparent]; got != span.Traceparent() {
		t.Errorf("traceparent = %q, want %q", got, span.Traceparent())
	}
	if inner.gotHeaders["x"] != "y" {
		t.Errorf("existing header dropped: %v", inner.gotHeaders)
	}
	if !result.Status.IsSuccess() {
		t.Errorf("result Status = %q, want the inner sender's success result passed through", result.Status)
	}
}

func TestTraceContextDecorator_NoSpanPassesThrough(t *testing.T) {
	inner := &capturingSender{result: benzene.Accepted[json.RawMessage](nil)}
	sender := TraceContextDecorator(inner)

	// No TraceMiddleware ran, so there is no span on the context: the call must still go through,
	// just without a traceparent (an unmeshed hop loses trace continuity, never the request).
	sender.Send(context.Background(), benzene.NewTopic("order:create"), map[string]string{"x": "y"}, []byte(`{}`))

	if _, present := inner.gotHeaders[headerTraceparent]; present {
		t.Errorf("traceparent should be absent with no span; headers = %v", inner.gotHeaders)
	}
	if inner.gotHeaders["x"] != "y" {
		t.Errorf("headers should pass through unchanged; got %v", inner.gotHeaders)
	}
}

func TestTraceContextDecorator_DoesNotOverwriteExistingTraceparent(t *testing.T) {
	for _, key := range []string{"traceparent", "TraceParent"} {
		t.Run(key, func(t *testing.T) {
			inner := &capturingSender{result: benzene.Accepted[json.RawMessage](nil)}
			sender := TraceContextDecorator(inner)

			ctx := contextWithSpan(context.Background(), Span{TraceID: "aaaa", SpanID: "bbbb"})
			existing := "00-caller-trace-caller-span-01"

			sender.Send(ctx, benzene.NewTopic("order:create"), map[string]string{key: existing}, []byte(`{}`))

			if got := inner.gotHeaders[key]; got != existing {
				t.Errorf("a caller-set %q was overwritten: %q, want %q", key, got, existing)
			}
		})
	}
}

func TestTraceContextDecorator_DoesNotMutateCallerHeaders(t *testing.T) {
	inner := &capturingSender{result: benzene.Accepted[json.RawMessage](nil)}
	sender := TraceContextDecorator(inner)

	ctx := contextWithSpan(context.Background(), Span{TraceID: "aaaa", SpanID: "bbbb"})
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
