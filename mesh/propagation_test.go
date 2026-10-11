package mesh

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/responseevents"
)

// These tests pin M2's outbound half (mesh.md §3, §7.1): every outbound Benzene message - a send
// with a reply, a one-way send, and a published event - carries this invocation's traceparent, with
// the caller's tracestate carried forward unchanged (wire-contracts.md §2).

const (
	inboundTrace      = "4bf92f3577b34da6a3ce929d0e0e4736"
	inboundParent     = "00f067aa0ba902b7"
	inboundTraceState = "vendor=opaque,other=1"
)

// recordingSender captures every outbound message's topic and headers, concurrency-safe.
type recordingSender struct {
	mu   sync.Mutex
	sent map[string]map[string]string
}

func (r *recordingSender) Send(_ context.Context, topic benzene.Topic, headers map[string]string, _ []byte) benzene.Result[json.RawMessage] {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[string]map[string]string{}
	}
	r.sent[topic.ID] = headers
	return benzene.Accepted[json.RawMessage](nil)
}

func (r *recordingSender) headersFor(t *testing.T, topic string) map[string]string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	headers, ok := r.sent[topic]
	if !ok {
		t.Fatalf("nothing was sent to %s; sent = %v", topic, r.sent)
	}
	return headers
}

// TestM2_EveryOutboundMessageCarriesTheTraceContext runs a traced invocation whose handler makes a
// one-way send and whose response is published as an event (responseevents over
// NewSenderPublisher), both through WithTraceContext, and checks each carries this invocation's
// span as parent and the inbound tracestate.
func TestM2_EveryOutboundMessageCarriesTheTraceContext(t *testing.T) {
	outbound := &recordingSender{}
	sender := WithTraceContext(outbound)

	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("order:create"), benzene.Handler[echoRequest, echoResponse](
		func(ctx context.Context, req echoRequest) benzene.Result[echoResponse] {
			// A one-way send (an SQS/SNS-style Client is a client.Sender like any other).
			sender.Send(ctx, benzene.NewTopic("audit:record"), map[string]string{"x-correlation-id": "c-1"}, []byte(`{}`))
			return benzene.Ok(echoResponse(req))
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	exporter := &captureExporter{}
	pipeline := benzene.NewPipeline(
		TraceMiddleware(ServiceInfo{Service: "orders", InstanceID: "orders-1"}, exporter),
		responseevents.Middleware(responseevents.NewSenderPublisher(sender), []responseevents.Mapping{
			responseevents.Map("order:create", "order:created"),
		}),
		benzene.RouterMiddleware(registry),
	)
	ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), map[string]string{
		"traceparent": "00-" + inboundTrace + "-" + inboundParent + "-01",
		"tracestate":  inboundTraceState,
	}, []byte(`{"text":"hi"}`), nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !ic.Result.ResultStatus().IsSuccess() {
		t.Fatalf("result status = %q, want success", ic.Result.ResultStatus())
	}

	span := exporter.single(t)
	want := "00-" + inboundTrace + "-" + span.SpanID + "-01"
	for _, topic := range []string{"audit:record", "order:created"} {
		headers := outbound.headersFor(t, topic)
		if headers["traceparent"] != want {
			t.Errorf("%s traceparent = %q, want %q (this invocation's span as parent)", topic, headers["traceparent"], want)
		}
		if headers["tracestate"] != inboundTraceState {
			t.Errorf("%s tracestate = %q, want the inbound %q carried forward unchanged", topic, headers["tracestate"], inboundTraceState)
		}
	}
	if outbound.headersFor(t, "audit:record")["x-correlation-id"] != "c-1" {
		t.Error("the send's own headers were dropped")
	}
}

// spanSeenBy runs one traced invocation with the given inbound headers and returns the span the
// handler saw on its context.
func spanSeenBy(t *testing.T, headers map[string]string) Span {
	t.Helper()
	var seen Span
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("order:create"), benzene.Handler[echoRequest, echoResponse](
		func(ctx context.Context, req echoRequest) benzene.Result[echoResponse] {
			seen, _ = SpanFromContext(ctx)
			return benzene.Ok(echoResponse(req))
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	runTraced(t, registry, benzene.NewTopic("order:create"), headers)
	return seen
}

func TestTraceMiddleware_TraceStateTravelsWithTheJoinedTrace(t *testing.T) {
	t.Run("adopted when the traceparent joins", func(t *testing.T) {
		span := spanSeenBy(t, map[string]string{
			"traceparent": "00-" + inboundTrace + "-" + inboundParent + "-01",
			"tracestate":  inboundTraceState,
		})
		if span.TraceID != inboundTrace || span.TraceState != inboundTraceState {
			t.Errorf("span = %+v, want trace %s with tracestate %q", span, inboundTrace, inboundTraceState)
		}
	})
	t.Run("dropped with a malformed traceparent (a fresh trace has no vendor state)", func(t *testing.T) {
		span := spanSeenBy(t, map[string]string{"traceparent": "garbage", "tracestate": inboundTraceState})
		if span.TraceState != "" || span.TraceID == inboundTrace {
			t.Errorf("span = %+v, want a fresh trace with no tracestate", span)
		}
	})
}

func TestWithTraceContext_ReplacesForwardedTraceState(t *testing.T) {
	forwarded := map[string]string{"TraceState": "stale=1", "traceparent": "00-" + inboundTrace + "-" + inboundParent + "-01"}

	t.Run("span with tracestate wins", func(t *testing.T) {
		inner := &capturingSender{result: benzene.Accepted[json.RawMessage](nil)}
		ctx := contextWithSpan(context.Background(), Span{TraceID: inboundTrace, SpanID: "0123456789abcdef", TraceState: "fresh=2"})
		WithTraceContext(inner).Send(ctx, benzene.NewTopic("t"), forwarded, nil)
		if inner.gotHeaders["tracestate"] != "fresh=2" || inner.gotHeaders["TraceState"] != "" {
			t.Errorf("headers = %v, want only the span's tracestate", inner.gotHeaders)
		}
	})
	t.Run("span without tracestate sends none", func(t *testing.T) {
		inner := &capturingSender{result: benzene.Accepted[json.RawMessage](nil)}
		ctx := contextWithSpan(context.Background(), Span{TraceID: inboundTrace, SpanID: "0123456789abcdef"})
		WithTraceContext(inner).Send(ctx, benzene.NewTopic("t"), forwarded, nil)
		for name := range inner.gotHeaders {
			if strings.EqualFold(name, "tracestate") {
				t.Errorf("headers = %v, want no tracestate - the forwarded one belongs to another span's context", inner.gotHeaders)
			}
		}
	})
}
