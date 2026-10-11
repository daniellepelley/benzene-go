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
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
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
	svc := mustNew(t, "greeter", newRegistry(t),
		WithServiceVersion("1.0.0"),
		WithRoutes(httpbinding.Route{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}),
	)

	// Identity, the runtime and the wiring self-check flow into the Contract Document.
	if svc.Contract.Service != "greeter" || svc.Contract.ServiceVersion != "1.0.0" || svc.Contract.RegistrationHash == "" || svc.Contract.Runtime != "go" {
		t.Errorf("contract = %+v, want service greeter/1.0.0 on go with a hash", svc.Contract)
	}
	// New wires the whole profile, so profile.missing is omitted - and the meshed-service M1/M2,
	// which New does not wire, are never listed there.
	if p := svc.Contract.Profile; p == nil || p.Name != "cloud-service" || p.Missing != nil {
		t.Errorf("profile = %+v, want cloud-service with no missing", p)
	}
	if raw, _ := json.Marshal(svc.Contract.Profile); string(raw) != `{"name":"cloud-service"}` {
		t.Errorf("profile JSON = %s, want missing omitted", raw)
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
	var spec contract.Document
	if err := json.Unmarshal(specRec.Body.Bytes(), &spec); err != nil || spec.ContractHash != svc.Contract.ContractHash || spec.SchemaDialect != contract.SchemaDialect {
		t.Errorf("spec body = %s (err %v), want the Contract Document with hash %q", specRec.Body, err, svc.Contract.ContractHash)
	}
	if len(spec.Requests) != 1 || len(spec.Requests[0].HTTPMappings) != 1 || spec.Requests[0].HTTPMappings[0].Path != "/greet" {
		t.Errorf("spec requests = %+v, want greet with its POST /greet mapping", spec.Requests)
	}
	if spec.Service != "greeter" || spec.ServiceVersion != "1.0.0" || spec.MessageEndpoint != httpbinding.EnvelopePath {
		t.Errorf("spec identity = %s %s %s", spec.Service, spec.ServiceVersion, spec.MessageEndpoint)
	}
	if resp := invoke(t, svc.Handler, contract.TopicID, ""); resp.StatusCode != string(benzene.StatusOk) || !strings.Contains(resp.Body, svc.Contract.ContractHash) {
		t.Errorf("invoke benzene:spec = %s %s, want ok with the document", resp.StatusCode, resp.Body)
	}
	if rec := get(t, svc.Handler, httpbinding.SpecPath+"?type=openapi"); rec.Code != http.StatusNotImplemented {
		t.Errorf("GET %s?type=openapi = %d, want 501", httpbinding.SpecPath, rec.Code)
	}

	// The reserved benzene:mesh topic is gone (spec-0.4.0): the self-description is benzene:spec.
	if resp := invoke(t, svc.Handler, "benzene:mesh", "{}"); benzene.Status(resp.StatusCode).IsSuccess() {
		t.Errorf("invoke benzene:mesh status = %q, want a failure - no such topic", resp.StatusCode)
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

	// New wires the whole profile (R1-R5, R7) but deliberately not the meshed-service claim's M1
	// feeds or M2 propagation: the profile is satisfied, the mesh section names exactly what remains.
	if !svc.Report.Satisfied() || len(svc.Report.Unsatisfied()) != 0 {
		t.Errorf("profile Satisfied() = false (unsatisfied %+v), want the whole profile wired", svc.Report.Unsatisfied())
	}
	if svc.Report.Mesh.Satisfied() || svc.Report.Meshed() {
		t.Error("Mesh.Satisfied()/Meshed() = true, want false - New alone does not wire M1 feeds or M2")
	}
	if got := requirementIDs(svc.Report.Mesh.Unsatisfied()); len(got) != 2 || !got["M1"] || !got["M2"] {
		t.Errorf("Mesh.Unsatisfied() = %+v, want M1 and M2", svc.Report.Mesh.Unsatisfied())
	}
}

func mustNew(t *testing.T, name string, registry *benzene.Registry, opts ...Option) *Service {
	t.Helper()
	svc, err := New(name, registry, opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return svc
}

func TestNew_ContractIdentityOptions(t *testing.T) {
	svc := mustNew(t, "greeter", newRegistry(t), WithServiceVersion("2.0.0"), WithVersionScheme("semver"), WithDescription("Greets"))
	if svc.Contract.VersionScheme != "semver" || svc.Contract.Description != "Greets" || svc.Contract.ServiceVersion != "2.0.0" {
		t.Errorf("contract identity = %+v", svc.Contract)
	}
}

func TestNew_DerivationErrorsFailBeforeServing(t *testing.T) {
	if svc, err := New("", newRegistry(t)); err == nil || svc != nil {
		t.Errorf("New(\"\") = %v, %v; want an error and no service", svc, err)
	}
}

func TestNew_WithoutDescriptorDisablesR5(t *testing.T) {
	svc := mustNew(t, "greeter", newRegistry(t), WithoutDescriptor())

	// /benzene/spec is not mounted -> falls through to the app router as an unrouted GET -> 404.
	if rec := get(t, svc.Handler, httpbinding.SpecPath); rec.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want 404 when the descriptor is disabled", httpbinding.SpecPath, rec.Code)
	}
	// The reserved benzene:spec topic no longer has interception -> not-found (no handler registered).
	for _, topic := range []string{contract.TopicID} {
		if resp := invoke(t, svc.Handler, topic, "{}"); benzene.Status(resp.StatusCode).IsSuccess() {
			t.Errorf("invoke %s status = %q, want a failure when the descriptor is disabled", topic, resp.StatusCode)
		}
	}

	// With the self-description off, R5 is the one unsatisfied profile requirement - and the derived
	// document says so, with no meshed-service id beside it.
	if p := svc.Contract.Profile; strings.Join(p.Missing, ",") != "R5" {
		t.Errorf("profile missing = %v, want [R5]", p.Missing)
	}
	if svc.Report.Satisfied() {
		t.Error("Satisfied() = true, want false when the descriptor is disabled")
	}
	if unsat := requirementIDs(svc.Report.Unsatisfied()); len(unsat) != 1 || !unsat["R5"] {
		t.Errorf("Unsatisfied() = %+v, want only R5 when the descriptor is disabled", svc.Report.Unsatisfied())
	}
}

func TestNew_HealthReflectsChecks(t *testing.T) {
	failing := healthcheck.NamedCheck("db", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusFailed, Type: "db"}
	})
	svc := mustNew(t, "greeter", newRegistry(t), WithHealthChecks(failing))

	if rec := get(t, svc.Handler, httpbinding.HealthPath); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s = %d, want 503 when a check fails", httpbinding.HealthPath, rec.Code)
	}
	health := requirementByID(svc.Report, "R3")
	if !strings.Contains(health.Detail, "1 health check") {
		t.Errorf("R3 detail = %q, want it to mention 1 health check", health.Detail)
	}
}

func TestNew_PlacementAndContainer(t *testing.T) {
	container := benzene.NewContainer()
	svc := mustNew(t, "greeter", newRegistry(t),
		WithPlacement("aws", "us-east-1"),
		WithContainer(container),
	)
	if p := svc.Contract.Placement; p == nil || p.Cloud != "aws" || p.Region != "us-east-1" {
		t.Errorf("placement = %+v, want aws/us-east-1", p)
	}
	if svc.Builder.Container != container {
		t.Error("Builder.Container is not the supplied container")
	}
}

func TestNew_DetectsPlacementWhenUnset(t *testing.T) {
	t.Setenv("K_SERVICE", "greeter")
	svc := mustNew(t, "greeter", newRegistry(t))
	if p := svc.Contract.Placement; p == nil || p.Cloud != "gcp" {
		t.Errorf("placement = %+v, want the detected gcp", p)
	}
}

func TestNew_WithProducesDeclaresEventsAndSendsElseDegrades(t *testing.T) {
	t.Run("unset names events and sends in degraded", func(t *testing.T) {
		svc := mustNew(t, "greeter", newRegistry(t))
		if len(svc.Contract.Events) != 0 || len(svc.Contract.Sends) != 0 {
			t.Errorf("events/sends = %v/%v, want empty", svc.Contract.Events, svc.Contract.Sends)
		}
		if strings.Join(svc.Contract.Degraded, ",") != "events,sends" {
			t.Errorf("Degraded = %v, want [events sends]", svc.Contract.Degraded)
		}
	})

	t.Run("WithConsumes declares the sends, not degraded", func(t *testing.T) {
		outbound := contract.NewOutboundRegistry()
		if err := contract.RegisterSend[greetReq](outbound, benzene.NewTopic("payments:capture"), contract.WithResponse[greetRes]()); err != nil {
			t.Fatalf("RegisterSend() error = %v", err)
		}
		svc := mustNew(t, "greeter", newRegistry(t), WithConsumes(outbound))

		if len(svc.Contract.Sends) != 1 || svc.Contract.Sends[0].Topic != "payments:capture" || svc.Contract.Sends[0].Response == nil {
			t.Errorf("Contract sends = %+v, want payments:capture with its reply", svc.Contract.Sends)
		}
		if svc.Contract.Degraded != nil {
			t.Errorf("Degraded = %v, want none with a real OutboundRegistry supplied", svc.Contract.Degraded)
		}
	})
}

func TestProfileReport_StringForNewBuild(t *testing.T) {
	svc := mustNew(t, "greeter", newRegistry(t))
	s := svc.Report.String()
	// Descriptor on: R1-R5 and R7 all wired (6 of 6); the meshed-service M1 and M2 not (0 of 2).
	if !strings.Contains(s, "Cloud Service Profile: 6/6 requirements wired") || !strings.Contains(s, "R4") {
		t.Errorf("report String() = %q, want a 6/6 profile header and the R-numbered list", s)
	}
	if !strings.Contains(s, "Meshed service (mesh §7.1): 0/2 requirements wired") || !strings.Contains(s, "M2") {
		t.Errorf("report String() = %q, want a 0/2 meshed-service header and the M-numbered list", s)
	}
	// The profile's ids are R1-R5 and R7; R6 and R8 are retired and never reused.
	var ids []string
	for _, req := range svc.Report.Requirements {
		ids = append(ids, req.ID)
	}
	if strings.Join(ids, ",") != "R1,R2,R3,R4,R5,R7" {
		t.Errorf("profile requirement ids = %v, want R1-R5, R7", ids)
	}
	// R4 is the envelope endpoint (not R2 - R2 is the registry-handlers requirement).
	if !strings.Contains(requirementByID(svc.Report, "R4").Detail, "/benzene/invoke") {
		t.Errorf("R4 = %+v, want it to be the /benzene/invoke envelope endpoint", requirementByID(svc.Report, "R4"))
	}
}

func TestProfileReport_AllSatisfied(t *testing.T) {
	// Exercise the fully-meshed path (New alone never reaches it, since M1/M2 are the app's to wire).
	report := ProfileReport{
		Requirements: []Requirement{
			{ID: "R1", Name: "a", Satisfied: true, Detail: "d"},
			{ID: "R2", Name: "b", Satisfied: true, Detail: "d"},
		},
		Mesh: MeshedReport{Requirements: []Requirement{{ID: "M1", Name: "c", Satisfied: true, Detail: "d"}}},
	}
	if !report.Satisfied() || !report.Mesh.Satisfied() || !report.Meshed() {
		t.Error("Satisfied()/Mesh.Satisfied()/Meshed() = false, want true when every requirement is wired")
	}
	if len(report.Unsatisfied()) != 0 || len(report.Mesh.Unsatisfied()) != 0 {
		t.Errorf("Unsatisfied() = %+v / %+v, want empty", report.Unsatisfied(), report.Mesh.Unsatisfied())
	}
	if !strings.Contains(report.String(), "2/2 requirements wired") || !strings.Contains(report.String(), "1/1 requirements wired") {
		t.Errorf("String() = %q, want 2/2 and 1/1 headers", report.String())
	}
	// A wired mesh does not make an unwired profile meshed.
	report.Requirements[1].Satisfied = false
	if report.Meshed() {
		t.Error("Meshed() = true with R2 unwired, want false - a meshed service is a Cloud Service first")
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
