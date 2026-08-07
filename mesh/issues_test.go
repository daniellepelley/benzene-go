package mesh

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestClassifyIssue(t *testing.T) {
	tests := []struct {
		status, exceptionType, want string
	}{
		// 1. validation wins, even with an exception type present.
		{"bad-request", "", "validation"},
		{"validation-error", "", "validation"},
		{"validation-error", "System.ArgumentException", "validation"},
		// 2. exception type present (and not a validation status).
		{"service-unavailable", "System.TimeoutException", "exception"},
		{"not-found", "SomeException", "exception"},
		// 3. config-wiring statuses, and empty status (a wiring gap).
		{"not-found", "", "config-wiring"},
		{"unauthorized", "", "config-wiring"},
		{"forbidden", "", "config-wiring"},
		{"not-implemented", "", "config-wiring"},
		{"", "", "config-wiring"},
		// 4. dependency statuses.
		{"service-unavailable", "", "dependency"},
		{"timeout", "", "dependency"},
		{"too-many-requests", "", "dependency"},
		// 5. unexpected-error -> exception.
		{"unexpected-error", "", "exception"},
		// 6. any other failing status -> unclassified.
		{"conflict", "", "unclassified"},
		{"application-defined", "", "unclassified"},
	}
	for _, tt := range tests {
		t.Run(tt.status+"/"+tt.exceptionType, func(t *testing.T) {
			if got := ClassifyIssue(tt.status, tt.exceptionType); got != tt.want {
				t.Errorf("ClassifyIssue(%q, %q) = %q, want %q", tt.status, tt.exceptionType, got, tt.want)
			}
		})
	}
}

func TestIssueFingerprint(t *testing.T) {
	fp := IssueFingerprint("orders", "order:create", "v2", "exception", "System.TimeoutException")
	if len(fp) != 32 {
		t.Errorf("fingerprint %q has length %d, want 32 (16 bytes hex)", fp, len(fp))
	}
	// Deterministic for the same inputs.
	if fp != IssueFingerprint("orders", "order:create", "v2", "exception", "System.TimeoutException") {
		t.Error("fingerprint is not deterministic")
	}
	// Any component change changes the fingerprint.
	if fp == IssueFingerprint("orders", "order:create", "", "exception", "System.TimeoutException") {
		t.Error("version must participate in the fingerprint")
	}
	if fp == IssueFingerprint("billing", "order:create", "v2", "exception", "System.TimeoutException") {
		t.Error("service must participate in the fingerprint")
	}
	// Lowercase hex only.
	for _, r := range fp {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("fingerprint %q is not lowercase hex", fp)
		}
	}
}

type fakeRecorder struct{ occurrences []IssueOccurrence }

func (f *fakeRecorder) Record(o IssueOccurrence) { f.occurrences = append(f.occurrences, o) }

func runIssueMiddleware(t *testing.T, recorder IssueRecorder, result benzene.ResultInfo, headers map[string]string) {
	t.Helper()
	terminal := func(_ context.Context, ic *benzene.InvocationContext, _ func(context.Context) error) error {
		ic.Result = result
		return nil
	}
	pipeline := benzene.NewPipeline(IssueMiddleware(ServiceInfo{Service: "orders"}, recorder), terminal)
	ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), headers, nil, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("pipeline.Run() error = %v", err)
	}
}

func TestIssueMiddleware_RecordsFailuresSkipsSuccesses(t *testing.T) {
	tests := []struct {
		name       string
		result     benzene.ResultInfo
		wantRecord bool
	}{
		{"framework failure recorded", benzene.ServiceUnavailable[any]("down"), true},
		{"application-defined failure recorded", benzene.Fail[any](benzene.Status("custom-fail"), "x"), true},
		{"success not recorded", benzene.Ok[any](struct{}{}), false},
		{"application-defined success not recorded", benzene.Result[any]{Status: benzene.Status("partial"), Payload: new(any)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeRecorder{}
			runIssueMiddleware(t, recorder, tt.result, map[string]string{})
			if got := len(recorder.occurrences) > 0; got != tt.wantRecord {
				t.Errorf("recorded = %v, want %v", got, tt.wantRecord)
			}
		})
	}
}

func TestIssueMiddleware_NilResultIsAWiringGap(t *testing.T) {
	recorder := &fakeRecorder{}
	// A terminal that never sets a result (a pipeline with no router) leaves ic.Result nil.
	pipeline := benzene.NewPipeline(IssueMiddleware(ServiceInfo{Service: "orders"}, recorder))
	ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), map[string]string{}, nil, nil)
	_ = pipeline.Run(context.Background(), ic)

	if len(recorder.occurrences) != 1 || recorder.occurrences[0].Status != "" {
		t.Errorf("occurrences = %+v, want one with an empty status (a wiring gap)", recorder.occurrences)
	}
}

func TestIssueMiddleware_FallsBackToTheTraceparentHeaderWhenNoSpan(t *testing.T) {
	// With no TraceMiddleware (no span on the context), the exemplar comes from the inbound
	// traceparent header.
	recorder := &fakeRecorder{}
	headers := map[string]string{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	runIssueMiddleware(t, recorder, benzene.ServiceUnavailable[any]("down"), headers)
	if len(recorder.occurrences) != 1 || recorder.occurrences[0].TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("occurrence = %+v, want the traceparent trace id as the exemplar", recorder.occurrences)
	}
}

func TestIssueMiddleware_ExemplarMatchesTheTraceFeedIdAtAnEntryPoint(t *testing.T) {
	// At an entry point (no inbound traceparent), TraceMiddleware mints a fresh trace id and puts
	// its span on the context. The issue exemplar must be that same id - the two feeds compose
	// over one trace - not empty.
	traceExp := &captureExporter{}
	recorder := &fakeRecorder{}
	info := ServiceInfo{Service: "orders"}
	terminal := func(_ context.Context, ic *benzene.InvocationContext, _ func(context.Context) error) error {
		ic.Result = benzene.ServiceUnavailable[any]("down")
		return nil
	}
	pipeline := benzene.NewPipeline(TraceMiddleware(info, traceExp), IssueMiddleware(info, recorder), terminal)
	ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), map[string]string{}, nil, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("pipeline.Run() error = %v", err)
	}

	if len(recorder.occurrences) != 1 || len(traceExp.events) != 1 {
		t.Fatalf("occurrences=%d trace events=%d, want 1 each", len(recorder.occurrences), len(traceExp.events))
	}
	exemplar := recorder.occurrences[0].TraceID
	if exemplar == "" {
		t.Fatal("issue exemplar traceID is empty at an entry point; want the trace feed's fresh id")
	}
	if exemplar != traceExp.events[0].TraceID {
		t.Errorf("issue exemplar = %q, want the trace event's id %q", exemplar, traceExp.events[0].TraceID)
	}
}

func TestIssueMiddleware_NilExporterIsPassThrough(t *testing.T) {
	// A nil exporter must not panic and must not affect the invocation.
	mw := IssueMiddleware(ServiceInfo{Service: "orders"}, nil)
	ic := benzene.NewInvocationContext(benzene.NewTopic("t"), map[string]string{}, nil, nil)
	called := false
	if err := mw(context.Background(), ic, func(context.Context) error { called = true; return nil }); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !called {
		t.Error("nil-exporter middleware must call next")
	}
}

type issueCaptureSender struct {
	mu      sync.Mutex
	batches []IssueBatch
}

func (c *issueCaptureSender) Send(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
	var batch IssueBatch
	if err := json.Unmarshal(message, &batch); err != nil {
		return benzene.BadRequest[json.RawMessage](err.Error())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if topic.String() != TopicIssues {
		return benzene.BadRequest[json.RawMessage]("wrong topic " + topic.String())
	}
	c.batches = append(c.batches, batch)
	return benzene.Ok(json.RawMessage(`{}`))
}

func (c *issueCaptureSender) last() IssueBatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.batches) == 0 {
		return IssueBatch{}
	}
	return c.batches[len(c.batches)-1]
}

func TestPushIssueExporter_AggregatesDeltasAndFlushes(t *testing.T) {
	sender := &issueCaptureSender{}
	at := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	exporter := NewPushIssueExporter(sender, "orders", PushIssueExporterOptions{FlushInterval: time.Hour})
	defer exporter.Close()

	occ := IssueOccurrence{Service: "orders", Topic: "order:create", Status: "service-unavailable", TraceID: "t1", At: at}
	exporter.Record(occ)
	occ.TraceID, occ.At = "t2", at.Add(time.Minute)
	exporter.Record(occ)

	exporter.Flush()
	batch := sender.last()
	if batch.Service != "orders" || len(batch.Issues) != 1 {
		t.Fatalf("batch = %+v, want one issue for orders", batch)
	}
	issue := batch.Issues[0]
	if issue.Count != 2 {
		t.Errorf("count = %d, want 2 (delta of two occurrences)", issue.Count)
	}
	if issue.Classification != "dependency" {
		t.Errorf("classification = %q, want dependency", issue.Classification)
	}
	if len(issue.ExemplarTraceIds) != 2 || issue.ExemplarTraceIds[0] != "t1" {
		t.Errorf("exemplars = %v, want [t1 t2]", issue.ExemplarTraceIds)
	}
	if issue.Fingerprint == "" {
		t.Error("issue must carry a fingerprint")
	}

	// After a flush the window resets: the next flush is the empty liveness batch.
	exporter.Flush()
	if batch := sender.last(); batch.Service != "orders" || len(batch.Issues) != 0 {
		t.Errorf("post-reset flush = %+v, want the empty liveness batch", batch)
	}
}

type foreignResult struct{ status benzene.Status }

func (f foreignResult) ResultStatus() benzene.Status { return f.status }
func (f foreignResult) ResultErrors() []string       { return nil }
func (f foreignResult) ResultPayload() any           { return nil }

func TestIssueWorthRecording_FallsBackToStatusForForeignResult(t *testing.T) {
	// A ResultInfo that does not implement ResultIsSuccessful falls back to the status class.
	if !issueWorthRecording(foreignResult{status: benzene.StatusServiceUnavailable}) {
		t.Error("a foreign failure result should be recorded")
	}
	if issueWorthRecording(foreignResult{status: benzene.StatusOk}) {
		t.Error("a foreign success result should not be recorded")
	}
}

func TestPushIssueExporter_ExemplarsCapAtThree(t *testing.T) {
	sender := &issueCaptureSender{}
	exporter := NewPushIssueExporter(sender, "orders", PushIssueExporterOptions{FlushInterval: time.Hour})
	defer exporter.Close()
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		exporter.Record(IssueOccurrence{Service: "orders", Topic: "t", Status: "timeout", TraceID: id, At: time.Now()})
	}
	exporter.Flush()
	issue := sender.last().Issues[0]
	if len(issue.ExemplarTraceIds) != 3 || issue.ExemplarTraceIds[0] != "t2" {
		t.Errorf("exemplars = %v, want the newest 3 [t2 t3 t4]", issue.ExemplarTraceIds)
	}
}

func TestPushIssueExporter_TickerFlushesOnInterval(t *testing.T) {
	sender := &issueCaptureSender{}
	// A short interval and default clock: the background loop flushes without an explicit call.
	exporter := NewPushIssueExporter(sender, "orders", PushIssueExporterOptions{FlushInterval: 5 * time.Millisecond})
	defer exporter.Close()
	exporter.Record(IssueOccurrence{Service: "orders", Topic: "t", Status: "timeout", At: time.Now()})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sender.last().Issues) == 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("the background loop did not flush on its interval")
}

func TestPushIssueExporter_DefaultsAreApplied(t *testing.T) {
	// Zero options: the constructor fills in the default interval and clock, and the exporter
	// works end to end.
	sender := &issueCaptureSender{}
	exporter := NewPushIssueExporter(sender, "orders", PushIssueExporterOptions{})
	exporter.Record(IssueOccurrence{Service: "orders", Topic: "t", Status: "timeout", At: time.Now()})
	exporter.Close() // flushes the tail
	if len(sender.last().Issues) != 1 {
		t.Errorf("exporter with default options did not flush; last = %+v", sender.last())
	}
}

func TestPushIssueExporter_NilSenderAndNilSafety(t *testing.T) {
	if NewPushIssueExporter(nil, "orders", PushIssueExporterOptions{}) != nil {
		t.Error("a nil sender must yield a nil exporter")
	}
	if NewPushIssueExporter(&issueCaptureSender{}, "", PushIssueExporterOptions{}) != nil {
		t.Error("an empty service must yield a nil exporter")
	}
	var nilExporter *PushIssueExporter
	nilExporter.Record(IssueOccurrence{}) // must not panic
	nilExporter.Flush()
	nilExporter.Close()
}

func TestPushIssueExporter_CloseFlushesTail(t *testing.T) {
	sender := &issueCaptureSender{}
	exporter := NewPushIssueExporter(sender, "orders", PushIssueExporterOptions{FlushInterval: time.Hour})
	exporter.Record(IssueOccurrence{Service: "orders", Topic: "t", Status: "timeout", At: time.Now()})
	exporter.Close()

	if batch := sender.last(); len(batch.Issues) != 1 {
		t.Errorf("Close should flush the tail; last batch = %+v", batch)
	}
}
