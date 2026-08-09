package cloudservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/wire"
)

type greetReq struct {
	Name string `json:"name"`
}
type greetRes struct {
	Message string `json:"message"`
}

func newRegistry(t *testing.T) *benzene.Registry {
	t.Helper()
	r := benzene.NewRegistry()
	if err := benzene.Register(r, benzene.NewTopic("greet"), benzene.Handler[greetReq, greetRes](
		func(_ context.Context, req greetReq) benzene.Result[greetRes] {
			return benzene.Ok(greetRes{Message: "hi " + req.Name})
		})); err != nil {
		t.Fatalf("register: %v", err)
	}
	return r
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func invoke(t *testing.T, h http.Handler, topic, body string) wire.Response {
	t.Helper()
	env, err := wire.MarshalRequest(wire.Request{Topic: topic, Headers: map[string]string{}, Body: body})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpbinding.EnvelopePath, bytes.NewReader(env)))
	resp, err := wire.UnmarshalResponse(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("unmarshal envelope response %s: %v", rec.Body.String(), err)
	}
	return resp
}

func TestNew_ServesReservedSurfaceAndAppRoutes(t *testing.T) {
	svc := New("greeter", newRegistry(t),
		WithServiceVersion("1.0.0"),
		WithBinding("http"),
		WithRoutes(httpbinding.Route{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}),
	)

	// Identity flows into the descriptor.
	if svc.Descriptor.Service != "greeter" || svc.Descriptor.ServiceVersion != "1.0.0" || svc.Descriptor.DescriptorHash == "" {
		t.Errorf("descriptor = %+v, want service greeter/1.0.0 with a hash", svc.Descriptor)
	}

	// R3: health over HTTP.
	if rec := get(t, svc.Handler, httpbinding.HealthPath); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d, want 200; body %s", httpbinding.HealthPath, rec.Code, rec.Body)
	}

	// R5: derived spec over HTTP, carrying the same hash.
	specRec := get(t, svc.Handler, httpbinding.SpecPath)
	if specRec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", httpbinding.SpecPath, specRec.Code)
	}
	var spec mesh.Descriptor
	if err := json.Unmarshal(specRec.Body.Bytes(), &spec); err != nil || spec.DescriptorHash != svc.Descriptor.DescriptorHash {
		t.Errorf("spec body = %s (err %v), want the descriptor with hash %q", specRec.Body, err, svc.Descriptor.DescriptorHash)
	}

	// R6: the reserved benzene:mesh topic returns the descriptor over the envelope path.
	if resp := invoke(t, svc.Handler, mesh.TopicID, "{}"); !benzene.Status(resp.StatusCode).IsSuccess() {
		t.Errorf("invoke benzene:mesh status = %q, want success", resp.StatusCode)
	}

	// R2 + app route: the envelope path dispatches an application topic.
	if resp := invoke(t, svc.Handler, "greet", `{"name":"Ann"}`); !strings.Contains(resp.Body, "hi Ann") {
		t.Errorf("invoke greet body = %q, want to contain 'hi Ann'", resp.Body)
	}
	// And the native app route.
	rec := httptest.NewRecorder()
	svc.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/greet", strings.NewReader(`{"name":"Bo"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hi Bo") {
		t.Errorf("POST /greet = %d %s, want 200 with 'hi Bo'", rec.Code, rec.Body)
	}

	if !svc.Report.Satisfied() || len(svc.Report.Unsatisfied()) != 0 {
		t.Errorf("report = %+v, want fully satisfied", svc.Report)
	}
}

func TestNew_WithoutDescriptorDisablesR5R6(t *testing.T) {
	svc := New("greeter", newRegistry(t), WithoutDescriptor())

	// /benzene/spec is not mounted -> falls through to the app router as an unrouted GET -> 404.
	if rec := get(t, svc.Handler, httpbinding.SpecPath); rec.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want 404 when the descriptor is disabled", httpbinding.SpecPath, rec.Code)
	}
	// The reserved benzene:mesh topic no longer has interception -> not-found (no handler registered).
	if resp := invoke(t, svc.Handler, mesh.TopicID, "{}"); benzene.Status(resp.StatusCode).IsSuccess() {
		t.Errorf("invoke benzene:mesh status = %q, want a failure when the descriptor is disabled", resp.StatusCode)
	}

	// Required surfaces still hold; the two descriptor surfaces are reported unsatisfied by choice.
	if !svc.Report.Satisfied() {
		t.Error("report.Satisfied() = false, want true (required surfaces still present)")
	}
	unsat := map[string]bool{}
	for _, s := range svc.Report.Unsatisfied() {
		unsat[s.Name] = true
	}
	if !unsat["mesh-descriptor"] || !unsat["derived-spec"] {
		t.Errorf("Unsatisfied() = %+v, want mesh-descriptor and derived-spec", svc.Report.Unsatisfied())
	}
}

func TestNew_HealthReflectsChecks(t *testing.T) {
	failing := healthcheck.CheckFunc{CheckName: "db", Fn: func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusFailed, Type: "db"}
	}}
	svc := New("greeter", newRegistry(t), WithHealthChecks(failing))

	if rec := get(t, svc.Handler, httpbinding.HealthPath); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s = %d, want 503 when a check fails", httpbinding.HealthPath, rec.Code)
	}
	health := surfaceByName(svc.Report, "health")
	if !strings.Contains(health.Detail, "1 health check") {
		t.Errorf("health surface detail = %q, want it to mention 1 health check", health.Detail)
	}
}

func TestNew_PlacementInstanceAndContainer(t *testing.T) {
	container := benzene.NewContainer()
	svc := New("greeter", newRegistry(t),
		WithPlacement("aws", "us-east-1"),
		WithInstanceID("greeter-7"),
		WithContainer(container),
	)
	if svc.Descriptor.Placement.Cloud != "aws" || svc.Descriptor.Placement.Region != "us-east-1" {
		t.Errorf("placement = %+v, want aws/us-east-1", svc.Descriptor.Placement)
	}
	if svc.Descriptor.InstanceID != "greeter-7" {
		t.Errorf("instanceID = %q, want greeter-7", svc.Descriptor.InstanceID)
	}
	if svc.Builder.Container != container {
		t.Error("Builder.Container is not the supplied container")
	}
}

func TestProfileReport_StringAndKinds(t *testing.T) {
	svc := New("greeter", newRegistry(t))
	s := svc.Report.String()
	if !strings.Contains(s, "envelope-invoke") || !strings.Contains(s, "all required surfaces present") {
		t.Errorf("report String() = %q, want the surface list and a satisfied header", s)
	}
	// Informational surfaces never count against conformance.
	feeds := surfaceByName(svc.Report, "mesh-outbound-feeds")
	if feeds.Kind != Informational || feeds.Satisfied {
		t.Errorf("mesh-outbound-feeds = %+v, want informational + unsatisfied", feeds)
	}
	for _, k := range []SurfaceKind{Required, Recommended, Informational} {
		if k.String() == "" {
			t.Errorf("SurfaceKind %d has empty String()", k)
		}
	}
}

func TestProfileReport_UnsatisfiedRequired(t *testing.T) {
	// New never leaves a Required surface unsatisfied, so exercise the report type directly to cover
	// the "missing required" path (Satisfied=false and its rendering).
	report := ProfileReport{Surfaces: []Surface{
		{Name: "envelope-invoke", Kind: Required, Satisfied: false, Detail: "not mounted"},
		{Name: "derived-spec", Kind: Recommended, Satisfied: true, Detail: "ok"},
	}}
	if report.Satisfied() {
		t.Error("Satisfied() = true, want false when a required surface is missing")
	}
	unsat := report.Unsatisfied()
	if len(unsat) != 1 || unsat[0].Name != "envelope-invoke" {
		t.Errorf("Unsatisfied() = %+v, want just the required envelope-invoke", unsat)
	}
	if !strings.Contains(report.String(), "MISSING required surfaces") {
		t.Errorf("String() = %q, want the MISSING header", report.String())
	}
}

func surfaceByName(r ProfileReport, name string) Surface {
	for _, s := range r.Surfaces {
		if s.Name == name {
			return s
		}
	}
	return Surface{}
}
