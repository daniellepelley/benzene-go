package cloudserviceprobe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeService is a configurable in-memory Benzene Cloud Service. Zero-valued fields fall back to a
// fully-conformant default, so a test overrides only the surface it is exercising.
type fakeService struct {
	healthPath, specPath, invokePath string

	healthStatus int
	healthBody   string
	specStatus   int
	specBody     string
	invokeStatus int
	invokeBody   string
	meshStatus   int
	meshBody     string

	lastTraceparent string
	sawInvoke       bool
}

const (
	defHealthBody = `{"isHealthy":true,"healthChecks":{}}`
	defSpecBody   = `{"openapi":"3.0.0","info":{}}`
	defInvokeBody = `{"statusCode":"200","headers":{},"body":"{\"isHealthy\":true}"}`
	defMeshBody   = `{"statusCode":"200","headers":{},"body":"{\"service\":\"orders\",\"topics\":[{\"topic\":\"order:create\"}]}"}`
)

func (f *fakeService) start(t *testing.T) string {
	t.Helper()
	if f.healthPath == "" {
		f.healthPath = HealthPath
	}
	if f.specPath == "" {
		f.specPath = SpecPath
	}
	if f.invokePath == "" {
		f.invokePath = InvokePath
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == f.healthPath:
			write(w, or(f.healthStatus, 200), or2(f.healthBody, defHealthBody))
		case r.Method == http.MethodGet && r.URL.Path == f.specPath:
			write(w, or(f.specStatus, 200), or2(f.specBody, defSpecBody))
		case r.Method == http.MethodPost && r.URL.Path == f.invokePath:
			f.sawInvoke = true
			f.lastTraceparent = r.Header.Get("traceparent")
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "benzene:mesh") {
				write(w, or(f.meshStatus, 200), or2(f.meshBody, defMeshBody))
			} else {
				write(w, or(f.invokeStatus, 200), or2(f.invokeBody, defInvokeBody))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func write(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func or(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func or2(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// verdictOf returns the verdict for requirement id in the report.
func verdictOf(t *testing.T, report *Report, id string) Requirement {
	t.Helper()
	for _, req := range report.Requirements {
		if req.ID == id {
			return req
		}
	}
	t.Fatalf("requirement %s not found in report", id)
	return Requirement{}
}

func assertVerdict(t *testing.T, report *Report, id string, want Verdict) {
	t.Helper()
	if got := verdictOf(t, report, id).Verdict; got != want {
		t.Errorf("%s verdict = %q, want %q (reason: %s)", id, got, want, verdictOf(t, report, id).Reason)
	}
}

func TestRun_FullyConformantIsSatisfiedExceptStructurallyUnobservable(t *testing.T) {
	svc := &fakeService{}
	report := Run(context.Background(), nil, svc.start(t))

	for _, id := range []string{"R1", "R2", "R3", "R4", "R5", "R6", "R7"} {
		assertVerdict(t, report, id, Satisfied)
	}
	// R8 is never observable by a single-service probe; with the default traceparent probe on and
	// R4/R6 both passing, its reason carries the labeled weak non-breakage note.
	assertVerdict(t, report, "R8", Inconclusive)
	if !strings.Contains(verdictOf(t, report, "R8").Reason, "weak bonus signal") {
		t.Errorf("R8 reason = %q, want the weak-bonus-signal note", verdictOf(t, report, "R8").Reason)
	}
	if len(report.Requirements) != 8 {
		t.Errorf("report has %d requirements, want 8", len(report.Requirements))
	}
	if report.IsFullyConformant() {
		t.Error("IsFullyConformant() = true, want false - R8 is inherently inconclusive")
	}
	if got := report.Inconclusive(); len(got) != 1 || got[0] != "R8" {
		t.Errorf("Inconclusive() = %v, want [R8]", got)
	}
	if got := report.NotSatisfied(); len(got) != 0 {
		t.Errorf("NotSatisfied() = %v, want none", got)
	}
	// The default probe attached a traceparent to the invoke/mesh calls.
	if svc.lastTraceparent == "" {
		t.Error("service saw no traceparent header, but the default probe should attach one")
	}
}

func TestRun_UnreachableServiceIsHonestlyNotSatisfied(t *testing.T) {
	// A server that is closed immediately: nothing responds.
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	report := Run(context.Background(), nil, url)
	assertVerdict(t, report, "R1", NotSatisfied) // nothing reachable
	assertVerdict(t, report, "R3", NotSatisfied)
	assertVerdict(t, report, "R4", NotSatisfied)
	assertVerdict(t, report, "R5", NotSatisfied)
	assertVerdict(t, report, "R6", NotSatisfied)
	assertVerdict(t, report, "R2", Inconclusive) // no descriptor to read topics from
	assertVerdict(t, report, "R7", NotSatisfied) // default paths, but R3-R6 failed there
	assertVerdict(t, report, "R8", Inconclusive)
}

func TestRun_MalformedBaseURLIsNotSatisfiedNotPanic(t *testing.T) {
	// An unparseable base URL makes request construction itself fail - reached=false, no panic.
	report := Run(context.Background(), nil, "http://%zz")
	assertVerdict(t, report, "R1", NotSatisfied)
	assertVerdict(t, report, "R4", NotSatisfied)
}

func TestRun_Health(t *testing.T) {
	t.Run("unexpected status is not satisfied", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{healthStatus: 500}).start(t))
		assertVerdict(t, report, "R3", NotSatisfied)
	})
	t.Run("503 with the health report is accepted (degradation is not non-conformance)", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{healthStatus: 503, healthBody: `{"isHealthy":false}`}).start(t))
		assertVerdict(t, report, "R3", Satisfied)
	})
	t.Run("missing isHealthy field is not satisfied", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{healthBody: `{"status":"ok"}`}).start(t))
		assertVerdict(t, report, "R3", NotSatisfied)
	})
}

func TestRun_InvokeNon200IsNotSatisfied(t *testing.T) {
	report := Run(context.Background(), nil, (&fakeService{invokeStatus: 500}).start(t))
	assertVerdict(t, report, "R4", NotSatisfied)
}

func TestRun_InvokeBadEnvelopeShapeIsNotSatisfied(t *testing.T) {
	report := Run(context.Background(), nil, (&fakeService{invokeBody: `{"statusCode":"200","headers":{}}`}).start(t)) // no body field
	req := verdictOf(t, report, "R4")
	if req.Verdict != NotSatisfied || !strings.Contains(req.Reason, "envelope shape") {
		t.Errorf("R4 = %+v, want NotSatisfied about the envelope shape", req)
	}
}

func TestRun_Spec(t *testing.T) {
	t.Run("empty object is not satisfied", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{specBody: `{}`}).start(t))
		assertVerdict(t, report, "R5", NotSatisfied)
	})
	t.Run("non-200 is not satisfied", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{specStatus: 404}).start(t))
		assertVerdict(t, report, "R5", NotSatisfied)
	})
}

func TestRun_Mesh(t *testing.T) {
	t.Run("descriptor without a service field fails R6 and leaves R2 inconclusive", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{meshBody: `{"statusCode":"200","headers":{},"body":"{\"topics\":[]}"}`}).start(t))
		assertVerdict(t, report, "R6", NotSatisfied)
		assertVerdict(t, report, "R2", Inconclusive)
	})
	t.Run("descriptor with zero topics satisfies R6 but leaves R2 inconclusive", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{meshBody: `{"statusCode":"200","headers":{},"body":"{\"service\":\"orders\",\"topics\":[]}"}`}).start(t))
		assertVerdict(t, report, "R6", Satisfied)
		assertVerdict(t, report, "R2", Inconclusive)
	})
	t.Run("non-200 on the mesh topic fails R6", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{meshStatus: 404}).start(t))
		assertVerdict(t, report, "R6", NotSatisfied)
	})
	t.Run("mesh envelope that is not the {statusCode,headers,body} shape fails R6", func(t *testing.T) {
		report := Run(context.Background(), nil, (&fakeService{meshBody: `{"not":"an envelope"}`}).start(t))
		req := verdictOf(t, report, "R6")
		if req.Verdict != NotSatisfied || !strings.Contains(req.Reason, "envelope shape") {
			t.Errorf("R6 = %+v, want NotSatisfied about the envelope shape", req)
		}
	})
}

func TestRun_NonDefaultPathsMakeR7Inconclusive(t *testing.T) {
	svc := &fakeService{healthPath: "/custom/health"}
	report := Run(context.Background(), nil, svc.start(t), WithHealthPath("/custom/health"))
	// The probe used the override, so health still checks out...
	assertVerdict(t, report, "R3", Satisfied)
	// ...but R7 can no longer tell what the service's own default is.
	assertVerdict(t, report, "R7", Inconclusive)
}

func TestRun_CustomInvokeAndSpecPathsAreUsed(t *testing.T) {
	svc := &fakeService{invokePath: "/api/invoke", specPath: "/api/spec"}
	report := Run(context.Background(), nil, svc.start(t),
		WithInvokePath("/api/invoke"), WithSpecPath("/api/spec"))
	// The probe used the overrides, so invoke, spec, and mesh (also on the invoke path) still check out.
	assertVerdict(t, report, "R4", Satisfied)
	assertVerdict(t, report, "R5", Satisfied)
	assertVerdict(t, report, "R6", Satisfied)
	// But R7 can't tell what the service itself defaults to once pointed at non-default paths.
	assertVerdict(t, report, "R7", Inconclusive)
}

func TestRun_WithoutTraceParentProbe(t *testing.T) {
	svc := &fakeService{}
	report := Run(context.Background(), nil, svc.start(t), WithoutTraceParentProbe())
	r8 := verdictOf(t, report, "R8")
	if r8.Verdict != Inconclusive || strings.Contains(r8.Reason, "bonus") {
		t.Errorf("R8 = %+v, want plain Inconclusive with no bonus note when the traceparent probe is off", r8)
	}
	if svc.sawInvoke && svc.lastTraceparent != "" {
		t.Errorf("traceparent = %q, want none when WithoutTraceParentProbe is set", svc.lastTraceparent)
	}
}

func TestRun_TraceParentBonusAbsentWhenACallFails(t *testing.T) {
	// traceparent probe is on (default) but invoke fails, so even the weak non-breakage signal is gone.
	report := Run(context.Background(), nil, (&fakeService{invokeStatus: 500}).start(t))
	r8 := verdictOf(t, report, "R8")
	if !strings.Contains(r8.Reason, "even the weak non-breakage signal is absent") {
		t.Errorf("R8 reason = %q, want the bonus-absent note", r8.Reason)
	}
}

func TestRun_ReadBodyErrorIsNotReached(t *testing.T) {
	// A transport whose response body errors on Read exercises the read-failure path in do().
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{}), Header: make(http.Header)}, nil
	})}
	report := Run(context.Background(), client, "http://example.test")
	assertVerdict(t, report, "R1", NotSatisfied) // no surface produced a readable body
}

func TestReport_HelpersAreEmptyForAllSatisfied(t *testing.T) {
	report := &Report{Requirements: []Requirement{
		{ID: "R1", Verdict: Satisfied},
		{ID: "R2", Verdict: Satisfied},
	}}
	if !report.IsFullyConformant() {
		t.Error("IsFullyConformant() = false, want true when all satisfied")
	}
	if len(report.NotSatisfied()) != 0 || len(report.Inconclusive()) != 0 {
		t.Error("NotSatisfied/Inconclusive should be empty when all satisfied")
	}
}

func TestParseEnvelope(t *testing.T) {
	tests := []struct {
		name, raw string
		wantOK    bool
		wantBody  string
	}{
		{"valid", `{"statusCode":"200","headers":{},"body":"inner"}`, true, "inner"},
		{"not json", `not json`, false, ""},
		{"not an object", `[1,2]`, false, ""},
		{"statusCode not a string", `{"statusCode":200,"headers":{},"body":"x"}`, false, ""},
		{"missing statusCode", `{"headers":{},"body":"x"}`, false, ""},
		{"headers not an object", `{"statusCode":"200","headers":"x","body":"x"}`, false, ""},
		{"body not a string", `{"statusCode":"200","headers":{},"body":5}`, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, reason, ok := parseEnvelope(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v (reason %q), want %v", ok, reason, tc.wantOK)
			}
			if ok && body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if !ok && reason == "" {
				t.Error("reason is empty for a failure")
			}
		})
	}
}

func TestParseDescriptor(t *testing.T) {
	tests := []struct {
		name, raw  string
		wantOK     bool
		wantSvc    string
		wantTopics int
	}{
		{"valid with topics", `{"service":"orders","topics":[{"a":1},{"b":2}]}`, true, "orders", 2},
		{"valid with null topics", `{"service":"orders","topics":null}`, true, "orders", 0},
		{"valid with absent topics", `{"service":"orders"}`, true, "orders", 0},
		{"empty service", `{"service":"","topics":[]}`, false, "", 0},
		{"missing service", `{"topics":[]}`, false, "", 0},
		{"not json", `nope`, false, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, topics, reason, ok := parseDescriptor(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v (reason %q), want %v", ok, reason, tc.wantOK)
			}
			if ok && (svc != tc.wantSvc || topics != tc.wantTopics) {
				t.Errorf("(service, topics) = (%q, %d), want (%q, %d)", svc, topics, tc.wantSvc, tc.wantTopics)
			}
		})
	}
}

func TestHasBoolField(t *testing.T) {
	cases := map[string]bool{
		`{"isHealthy":true}`:  true,
		`{"isHealthy":false}`: true,
		`{"isHealthy":"yes"}`: false,
		`{"other":true}`:      false,
		`not json`:            false,
	}
	for raw, want := range cases {
		if got := hasBoolField(raw, "isHealthy"); got != want {
			t.Errorf("hasBoolField(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestIsNonEmptyObject(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`: true,
		`{}`:      false,
		`[1]`:     false,
		`nope`:    false,
	}
	for raw, want := range cases {
		if got := isNonEmptyObject(raw); got != want {
			t.Errorf("isNonEmptyObject(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestSyntheticTraceParent(t *testing.T) {
	tp := syntheticTraceParent()
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || parts[3] != "01" {
		t.Errorf("syntheticTraceParent() = %q, want a valid 00-<32hex>-<16hex>-01 traceparent", tp)
	}
	if syntheticTraceParent() == tp {
		t.Error("two synthetic traceparents were identical, want random ids")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
