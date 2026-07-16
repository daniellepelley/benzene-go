package mesh

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"strings"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// TraceEvent is one pipeline invocation as seen by the mesh (mesh.md §5.2). It is
// semantic where a transport-level span is not: it carries the topic (+ version) and the
// Benzene status, not a URL and an HTTP code. TraceID/SpanID/ParentSpanID are the W3C
// traceparent fields (hex, 16/8/8 bytes), so mesh traces interleave with any existing
// OpenTelemetry pipeline instead of competing with it.
type TraceEvent struct {
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId,omitempty"`
	Service      string `json:"service,omitempty"`
	InstanceID   string `json:"instanceId,omitempty"`
	Topic        string `json:"topic"`
	TopicVersion string `json:"topicVersion,omitempty"`
	// Status is the Benzene status verbatim (empty when no downstream middleware produced
	// a Result - a pipeline without a router, which is a wiring gap the mesh reports
	// as-is rather than papering over).
	Status        string    `json:"status"`
	DurationMs    float64   `json:"durationMs"`
	StartedAt     time.Time `json:"startedAt"`
	CorrelationID string    `json:"correlationId,omitempty"`
}

// Exporter receives every TraceEvent the trace middleware produces. Implementations must
// be safe for concurrent use (one pipeline serves concurrent invocations) and should
// never block for long - and regardless of what they do, the middleware guarantees an
// exporter cannot affect the invocation it observed (a panic is swallowed, and the
// middleware has no error path to poison).
type Exporter interface {
	Export(ctx context.Context, event TraceEvent)
}

// TraceMiddleware observes every invocation that passes through it and hands the
// resulting TraceEvent to exporter after downstream middleware finishes. Register it
// outermost (before healthcheck/mesh/router interception) so it sees every invocation,
// including intercepted ones. Because the router converts missing handlers, conversion
// failures, and handler panics into Results rather than Go errors, every routed
// invocation produces a status - trace coverage is structural, not best-effort.
//
// A nil exporter returns a pass-through middleware: the trace feed is simply off, and the
// service behaves identically to an unmeshed one. This is the "reduced mesh" posture -
// each feed degrades independently, never the service.
//
// An incoming W3C traceparent header joins the existing trace (its trace-id is adopted
// and its parent-id recorded); a missing or malformed one starts a fresh trace. The
// x-correlation-id header, when present, is carried verbatim.
func TraceMiddleware(info ServiceInfo, exporter Exporter) benzene.Middleware {
	if exporter == nil {
		return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
			return next(ctx)
		}
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		started := time.Now()
		traceID, parentSpanID := parseTraceparent(ic.Headers["traceparent"])
		if traceID == "" {
			traceID = randHex(16)
		}
		event := TraceEvent{
			TraceID:       traceID,
			SpanID:        randHex(8),
			ParentSpanID:  parentSpanID,
			Service:       info.Service,
			InstanceID:    info.InstanceID,
			Topic:         ic.Topic.ID,
			TopicVersion:  ic.Topic.Version,
			StartedAt:     started.UTC(),
			CorrelationID: ic.Headers["x-correlation-id"],
		}

		// The span rides on the context so a handler can propagate it onto outbound calls
		// (SpanFromContext + Span.Traceparent) - the join that lets a collector derive
		// consumer edges from parentage.
		err := next(contextWithSpan(ctx, Span{TraceID: traceID, SpanID: event.SpanID}))

		event.DurationMs = float64(time.Since(started)) / float64(time.Millisecond)
		if ic.Result != nil {
			event.Status = string(ic.Result.ResultStatus())
		}
		export(ctx, exporter, event)
		return err
	}
}

// export shields the invocation from the exporter: a panicking exporter loses its own
// event, never the caller's response.
func export(ctx context.Context, exporter Exporter, event TraceEvent) {
	defer func() {
		_ = recover()
	}()
	exporter.Export(ctx, event)
}

// parseTraceparent extracts the trace-id and the caller's span-id ("parent-id") from a
// W3C traceparent header value, "00-<32 hex trace-id>-<16 hex parent-id>-<2 hex flags>".
// Absent or malformed values (wrong segment count/length, non-hex, or the all-zero ids
// the W3C spec defines as invalid) yield ("", ""), and the middleware starts a fresh
// trace - a bad caller header degrades correlation, never the invocation.
func parseTraceparent(header string) (traceID, parentSpanID string) {
	parts := strings.Split(header, "-")
	if len(parts) != 4 ||
		len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 ||
		!isHex(parts[0]) || !isHex(parts[1]) || !isHex(parts[2]) || !isHex(parts[3]) ||
		parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		return "", ""
	}
	return parts[1], parts[2]
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// randHex returns nBytes random bytes hex-encoded (nBytes must be a multiple of 8, as it
// is at both call sites). Trace/span ids need uniqueness, not unpredictability, so
// math/rand/v2's process-seeded generator is enough - and unlike crypto/rand it has no
// error path to handle.
func randHex(nBytes int) string {
	buf := make([]byte, nBytes)
	for i := 0; i+8 <= nBytes; i += 8 {
		binary.BigEndian.PutUint64(buf[i:], rand.Uint64())
	}
	return hex.EncodeToString(buf)
}
