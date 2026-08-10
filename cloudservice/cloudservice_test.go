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

	// New wires the synchronous HTTP surface (R1-R5, R7) but deliberately not R6's outbound feeds or
	// R8, so the honest report is NOT fully satisfied - it names exactly what remains.
	if svc.Report.Satisfied() {
		t.Error("report.Satisfied() = true, want false - New alone does not wire R6 feeds or R8")
	}
	unsat := requirementIDs(svc.Report.Unsatisfied())
	if !unsat["R6"] || !unsat["R8"] || unsat["R5"] {
		t.Errorf("Unsatisfied() = %+v, want R6 and R8 (R5 satisfied - descriptor is on)", svc.Report.Unsatisfied())
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

	// With the descriptor off, R5 joins R6 and R8 as unsatisfied.
	unsat := requirementIDs(svc.Report.Unsatisfied())
	if !unsat["R5"] || !unsat["R6"] || !unsat["R8"] {
		t.Errorf("Unsatisfied() = %+v, want R5, R6, R8 when the descriptor is disabled", svc.Report.Unsatisfied())
	}
}

func TestNew_HealthReflectsChecks(t *testing.T) {
	failing := healthcheck.NamedCheck("db", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusFailed, Type: "db"}
	})
	svc := New("greeter", newRegistry(t), WithHealthChecks(failing))

	if rec := get(t, svc.Handler, httpbinding.HealthPath); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s = %d, want 503 when a check fails", httpbinding.HealthPath, rec.Code)
	}
	health := requirementByID(svc.Report, "R3")
	if !strings.Contains(health.Detail, "1 health check") {
		t.Errorf("R3 detail = %q, want it to mention 1 health check", health.Detail)
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

func TestProfileReport_StringForNewBuild(t *testing.T) {
	svc := New("greeter", newRegistry(t))
	s := svc.Report.String()
	// Descriptor on: R1-R5 and R7 wired, R6 and R8 not - 6 of 8.
	if !strings.Contains(s, "6/8 requirements wired") || !strings.Contains(s, "R4") {
		t.Errorf("report String() = %q, want a 6/8 header and the R-numbered list", s)
	}
	if requirementByID(svc.Report, "R6").Satisfied || requirementByID(svc.Report, "R8").Satisfied {
		t.Error("R6 and R8 must be unsatisfied for a New-only build")
	}
	// R4 is the envelope endpoint (not R2 - R2 is the registry-handlers requirement).
	if !strings.Contains(requirementByID(svc.Report, "R4").Detail, "/benzene/invoke") {
		t.Errorf("R4 = %+v, want it to be the /benzene/invoke envelope endpoint", requirementByID(svc.Report, "R4"))
	}
}

func TestProfileReport_AllSatisfied(t *testing.T) {
	// Exercise the fully-satisfied path (New alone never reaches it, since R6/R8 are the app's to wire).
	report := ProfileReport{Requirements: []Requirement{
		{ID: "R1", Name: "a", Satisfied: true, Detail: "d"},
		{ID: "R2", Name: "b", Satisfied: true, Detail: "d"},
	}}
	if !report.Satisfied() {
		t.Error("Satisfied() = false, want true when every requirement is wired")
	}
	if len(report.Unsatisfied()) != 0 {
		t.Errorf("Unsatisfied() = %+v, want empty", report.Unsatisfied())
	}
	if !strings.Contains(report.String(), "2/2 requirements wired") {
		t.Errorf("String() = %q, want a 2/2 header", report.String())
	}
}

func requirementByID(r ProfileReport, id string) Requirement {
	for _, req := range r.Requirements {
		if req.ID == id {
			return req
		}
	}
	return Requirement{}
}

func requirementIDs(reqs []Requirement) map[string]bool {
	ids := map[string]bool{}
	for _, req := range reqs {
		ids[req.ID] = true
	}
	return ids
}
