// Package cloudserviceprobe is the external, black-box conformance checker for the Benzene Cloud
// Service Profile (docs/specification/cloud-service-profile.md §2, §5). It hits a running service
// over real HTTP from the outside and independently verifies what it can observe of R1-R8 - exactly
// as an operator or a CI job auditing an arbitrary service would, .NET, Go, or otherwise. It is the
// Go port of Benzene.CloudService.Probe.
//
// It is deliberately self-contained and depends on nothing else in this module: the profile is a
// language-neutral spec, so this tool must be usable against ANY Benzene Cloud Service reachable
// over HTTP - it therefore parses the wire shapes it checks (wire-contracts.md §1.2/§5, mesh.md §2)
// independently rather than importing this port's own models, and keeps its own copy of the
// /benzene/* default path constants rather than referencing httpbinding. Do not "fix" that into a
// shared reference; the independence is the point (it is what lets the same tool audit a non-Go
// service).
//
// Honesty rule: the result is tri-state (Satisfied / NotSatisfied / Inconclusive), never a bool. A
// single-service HTTP probe genuinely cannot verify everything - R8 (trace propagation) and half of
// R6 (register/heartbeat delivery to a collector) are not observable from one service in isolation,
// and R7 becomes unknowable the moment the caller points the probe at non-default paths. Those stay
// Inconclusive by design, never silently upgraded to Satisfied. Run never returns an error for an
// unreachable or non-conformant service: unreachability and shape mismatches are verdicts, not
// failures.
package cloudserviceprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The default service standard's well-known HTTP paths (design-principles.md §5.2, mandatory by
// default under the profile's R7). These deliberately duplicate httpbinding's constants rather than
// referencing them, so this package can audit any service (including non-Go ports) without a
// dependency on this port's own wiring - see the package doc.
const (
	// InvokePath is the wire-envelope endpoint (profile R4/R6).
	InvokePath = "/benzene/invoke"
	// SpecPath is the derived spec document endpoint (profile R5).
	SpecPath = "/benzene/spec"
	// HealthPath is the aggregated health check endpoint (profile R3).
	HealthPath = "/benzene/health"
)

// The synthetic envelopes the probe POSTs to the invoke endpoint: a healthcheck round-trip for R4,
// and the reserved mesh-descriptor topic for R6. Both use the reserved benzene: topics.
const (
	healthcheckEnvelope = `{"topic":"benzene:healthcheck","headers":{},"body":"{}"}`
	meshEnvelope        = `{"topic":"benzene:mesh","headers":{},"body":"{}"}`
)

// Verdict is the tri-state assessment an external probe can honestly reach for one requirement.
type Verdict string

const (
	// Satisfied means the probe independently observed evidence that the requirement holds.
	Satisfied Verdict = "satisfied"
	// NotSatisfied means the probe reached the service and observed that the requirement does not hold.
	NotSatisfied Verdict = "not-satisfied"
	// Inconclusive means the probe cannot determine the requirement from outside the service alone -
	// the surface was unreachable (cascading from another failure), or the requirement is inherently
	// unobservable by a single-service HTTP probe (R8, half of R6). Never treat it as a pass.
	Inconclusive Verdict = "inconclusive"
)

// Requirement is one profile requirement as independently assessed by a probe run.
type Requirement struct {
	// ID is the requirement id (R1-R8).
	ID string
	// Description is what the requirement mandates, in one line.
	Description string
	// Verdict is the probe's independently-observed verdict.
	Verdict Verdict
	// Reason is why the verdict is what it is - always populated, since an outside observer explaining
	// itself matters even more when it has no service-side word to fall back on.
	Reason string
}

// Report is the result of one probe run: an external, black-box, tri-state assessment of R1-R8 built
// entirely from what the probe observed over HTTP. It never trusts anything the service claims about
// itself (e.g. a profile field on its own descriptor).
type Report struct {
	// Requirements holds every profile requirement with its assessment, in R1-R8 order.
	Requirements []Requirement
}

// NotSatisfied returns the ids the probe positively observed as unmet.
func (r *Report) NotSatisfied() []string { return r.idsWith(NotSatisfied) }

// Inconclusive returns the ids the probe could not determine from outside the service.
func (r *Report) Inconclusive() []string { return r.idsWith(Inconclusive) }

func (r *Report) idsWith(verdict Verdict) []string {
	var ids []string
	for _, req := range r.Requirements {
		if req.Verdict == verdict {
			ids = append(ids, req.ID)
		}
	}
	return ids
}

// IsFullyConformant reports whether every requirement was observed as Satisfied. Because R8 (and
// half of R6) are structurally unobservable by a single-service probe, this is essentially never
// true for a real service - that is the honest ceiling of an external HTTP probe, not a bug. Read
// NotSatisfied and Inconclusive for the actual picture.
func (r *Report) IsFullyConformant() bool {
	for _, req := range r.Requirements {
		if req.Verdict != Satisfied {
			return false
		}
	}
	return true
}

// Option configures a probe Run.
type Option func(*config)

type config struct {
	invokePath      string
	specPath        string
	healthPath      string
	sendTraceParent bool
}

func defaults() config {
	return config{invokePath: InvokePath, specPath: SpecPath, healthPath: HealthPath, sendTraceParent: true}
}

func (c config) usesDefaultPaths() bool {
	return c.invokePath == InvokePath && c.specPath == SpecPath && c.healthPath == HealthPath
}

// WithInvokePath overrides the wire-envelope endpoint probed for R4/R6 (default InvokePath).
func WithInvokePath(path string) Option { return func(c *config) { c.invokePath = path } }

// WithSpecPath overrides the derived-spec endpoint probed for R5 (default SpecPath).
func WithSpecPath(path string) Option { return func(c *config) { c.specPath = path } }

// WithHealthPath overrides the health endpoint probed for R3 (default HealthPath).
func WithHealthPath(path string) Option { return func(c *config) { c.healthPath = path } }

// WithoutTraceParentProbe turns off the default-on synthetic traceparent header on the R4/R6 probe
// requests. The header is only ever a weak, explicitly-labeled non-breakage signal for R8 - it never
// upgrades R8 past Inconclusive - so disabling it only removes that note from R8's reason.
func WithoutTraceParentProbe() Option { return func(c *config) { c.sendTraceParent = false } }

// Run probes the service at baseURL and returns a tri-state assessment of R1-R8. It never returns an
// error for an unreachable or non-conformant service - those become verdicts. A nil client uses
// http.DefaultClient; the caller owns the client's timeout and any auth headers needed to reach the
// service, and the ctx cancels the run.
func Run(ctx context.Context, client *http.Client, baseURL string, opts ...Option) *Report {
	if client == nil {
		client = http.DefaultClient
	}
	cfg := defaults()
	for _, opt := range opts {
		opt(&cfg)
	}

	health := probeHealth(ctx, client, baseURL, cfg)
	invoke := probeInvoke(ctx, client, baseURL, cfg)
	spec := probeSpec(ctx, client, baseURL, cfg)
	mesh := probeMesh(ctx, client, baseURL, cfg)

	return &Report{Requirements: []Requirement{
		evaluateR1(health, invoke, spec, mesh),
		evaluateR2(mesh),
		health.req,
		invoke.req,
		spec.req,
		mesh.req,
		evaluateR7(cfg, health, invoke, spec, mesh),
		evaluateR8(cfg, invoke, mesh),
	}}
}

// probeOutcome is the internal result of probing one surface: its requirement verdict, whether the
// surface was reached at all (for the inferential R1), and - for the mesh probe - the descriptor's
// topic count.
type probeOutcome struct {
	req        Requirement
	reached    bool
	topicCount int
}

const (
	descR3 = "Health checks"
	descR4 = "Wire-envelope invocability"
	descR5 = "Derived spec"
	descR6 = "Mesh service-side feeds (observable half - see reason)"
)

// probeHealth is R3: GET the health path; satisfied iff 200 or 503 with a boolean isHealthy field
// (wire-contracts.md §5). 503 is accepted because a conformant service returns it when unhealthy
// with the same report body - runtime degradation is not a conformance failure (profile §4).
func probeHealth(ctx context.Context, client *http.Client, baseURL string, cfg config) probeOutcome {
	reached, status, body, failure := get(ctx, client, baseURL+cfg.healthPath)
	if !reached {
		return probeOutcome{req: req("R3", descR3, NotSatisfied, fmt.Sprintf("GET %s did not reach the service: %s", cfg.healthPath, failure))}
	}
	if status != 200 && status != 503 {
		return probeOutcome{req: req("R3", descR3, NotSatisfied, fmt.Sprintf("GET %s returned %d, expected 200 or 503", cfg.healthPath, status)), reached: true}
	}
	if !hasBoolField(body, "isHealthy") {
		return probeOutcome{req: req("R3", descR3, NotSatisfied, fmt.Sprintf("%d response at %s did not have a boolean 'isHealthy' field (wire-contracts.md §5)", status, cfg.healthPath)), reached: true}
	}
	return probeOutcome{req: req("R3", descR3, Satisfied, fmt.Sprintf("GET %s returned %d with a boolean 'isHealthy' field", cfg.healthPath, status)), reached: true}
}

// probeInvoke is R4: POST a synthetic healthcheck envelope; satisfied iff 200 with the {statusCode,
// headers, body} response shape (wire-contracts.md §1.2).
func probeInvoke(ctx context.Context, client *http.Client, baseURL string, cfg config) probeOutcome {
	reached, status, body, failure := post(ctx, client, baseURL+cfg.invokePath, healthcheckEnvelope, cfg.sendTraceParent)
	if !reached {
		return probeOutcome{req: req("R4", descR4, NotSatisfied, fmt.Sprintf("POST %s did not reach the service: %s", cfg.invokePath, failure))}
	}
	if status != 200 {
		return probeOutcome{req: req("R4", descR4, NotSatisfied, fmt.Sprintf("POST %s with a synthetic envelope returned %d, expected 200", cfg.invokePath, status)), reached: true}
	}
	if _, reason, ok := parseEnvelope(body); !ok {
		return probeOutcome{req: req("R4", descR4, NotSatisfied, fmt.Sprintf("200 response at %s did not have the {statusCode, headers, body} envelope shape (wire-contracts.md §1.2): %s", cfg.invokePath, reason)), reached: true}
	}
	return probeOutcome{req: req("R4", descR4, Satisfied, fmt.Sprintf("POST %s with a synthetic envelope round-tripped a valid {statusCode, headers, body} response", cfg.invokePath)), reached: true}
}

// probeSpec is R5: GET the spec path; satisfied iff 200 with a non-empty JSON object. No specific
// spec format (OpenAPI, AsyncAPI, or Benzene's own) is assumed - only that a real document is there.
func probeSpec(ctx context.Context, client *http.Client, baseURL string, cfg config) probeOutcome {
	reached, status, body, failure := get(ctx, client, baseURL+cfg.specPath)
	if !reached {
		return probeOutcome{req: req("R5", descR5, NotSatisfied, fmt.Sprintf("GET %s did not reach the service: %s", cfg.specPath, failure))}
	}
	if status != 200 {
		return probeOutcome{req: req("R5", descR5, NotSatisfied, fmt.Sprintf("GET %s returned %d, expected 200", cfg.specPath, status)), reached: true}
	}
	if !isNonEmptyObject(body) {
		return probeOutcome{req: req("R5", descR5, NotSatisfied, fmt.Sprintf("200 response at %s did not parse as a non-empty JSON object; the profile does not mandate a spec format, only that a real derived document is there", cfg.specPath)), reached: true}
	}
	return probeOutcome{req: req("R5", descR5, Satisfied, fmt.Sprintf("GET %s returned 200 with a non-empty JSON object body", cfg.specPath)), reached: true}
}

const meshCaveat = "Registration (mesh:register) and heartbeat (mesh:heartbeat) delivery to a collector cannot be observed by probing the service alone, so that half of R6 stays unverified even when the descriptor endpoint itself checks out - a passing descriptor check does not imply the whole of R6."

// probeMesh is R6 (observable half only): POST the reserved mesh topic; satisfied iff a 200 envelope
// wraps a descriptor with a non-empty "service" field (mesh.md §2). The descriptor's topic count is
// carried out for R2's inferential check.
func probeMesh(ctx context.Context, client *http.Client, baseURL string, cfg config) probeOutcome {
	reached, status, body, failure := post(ctx, client, baseURL+cfg.invokePath, meshEnvelope, cfg.sendTraceParent)
	if !reached {
		return probeOutcome{req: req("R6", descR6, NotSatisfied, fmt.Sprintf("POST %s with topic 'benzene:mesh' did not reach the service: %s. %s", cfg.invokePath, failure, meshCaveat))}
	}
	if status != 200 {
		return probeOutcome{req: req("R6", descR6, NotSatisfied, fmt.Sprintf("POST %s with topic 'benzene:mesh' returned %d, expected 200 (the reserved mesh topic does not appear to be served). %s", cfg.invokePath, status, meshCaveat)), reached: true}
	}
	inner, envReason, ok := parseEnvelope(body)
	if !ok {
		return probeOutcome{req: req("R6", descR6, NotSatisfied, fmt.Sprintf("200 response to the 'benzene:mesh' topic did not have the {statusCode, headers, body} envelope shape (wire-contracts.md §1.2): %s. %s", envReason, meshCaveat)), reached: true}
	}
	service, topics, descReason, ok := parseDescriptor(inner)
	if !ok {
		return probeOutcome{req: req("R6", descR6, NotSatisfied, fmt.Sprintf("200 response to the 'benzene:mesh' topic did not parse as a descriptor with a non-empty 'service' field (mesh.md §2): %s. %s", descReason, meshCaveat)), reached: true}
	}
	return probeOutcome{
		req:        req("R6", descR6, Satisfied, fmt.Sprintf("the reserved 'benzene:mesh' topic served a descriptor for service '%s' - the observable half of R6 checks out. %s", service, meshCaveat)),
		reached:    true,
		topicCount: topics,
	}
}

// evaluateR1 is inferential: satisfied iff anything responded at all, which proves a hosted pipeline
// exists behind a transport binding (an in-process-only pipeline would have nothing to reach).
func evaluateR1(health, invoke, spec, mesh probeOutcome) Requirement {
	const desc = "Hosted middleware pipeline"
	if health.reached || invoke.reached || spec.reached || mesh.reached {
		return req("R1", desc, Satisfied, "at least one probed surface responded over HTTP, which proves a hosted pipeline exists behind a transport binding - an in-process-only pipeline would have nothing for this probe to reach")
	}
	return req("R1", desc, NotSatisfied, "none of the health, invoke, spec, or mesh surfaces responded at all; no hosted pipeline was reachable at the configured base address")
}

// evaluateR2 is inferential from R6's descriptor topics only - the profile mandates no spec format,
// so topic counts can't be read from R5.
func evaluateR2(mesh probeOutcome) Requirement {
	const desc = "Message handlers via the registry"
	if mesh.req.Verdict != Satisfied {
		return req("R2", desc, Inconclusive, "no reachable mesh descriptor to read registered topics from, and the profile does not mandate a spec document format, so topic counts can't be read from the spec surface either - absence of evidence isn't evidence of absence")
	}
	if mesh.topicCount > 0 {
		return req("R2", desc, Satisfied, fmt.Sprintf("the mesh descriptor lists %d registered topic(s) (mesh.md §2), which can only be populated from the handler registry", mesh.topicCount))
	}
	return req("R2", desc, Inconclusive, "the mesh descriptor is reachable but lists no topics; a service can legitimately have zero handlers registered so far - absence of evidence isn't evidence of absence")
}

// evaluateR7 is only assessable when the probe itself used the defaults; otherwise it can't tell what
// the service defaults to.
func evaluateR7(cfg config, health, invoke, spec, mesh probeOutcome) Requirement {
	const desc = "Default service standard paths"
	if !cfg.usesDefaultPaths() {
		return req("R7", desc, Inconclusive, "the probe was configured with non-default path(s), so it cannot tell whether the service itself defaults to /benzene/invoke, /benzene/spec, and /benzene/health - it was told to look elsewhere")
	}
	var failing []string
	if health.req.Verdict != Satisfied {
		failing = append(failing, "R3 (health)")
	}
	if invoke.req.Verdict != Satisfied {
		failing = append(failing, "R4 (invoke)")
	}
	if spec.req.Verdict != Satisfied {
		failing = append(failing, "R5 (spec)")
	}
	if mesh.req.Verdict != Satisfied {
		failing = append(failing, "R6 (mesh)")
	}
	if len(failing) == 0 {
		return req("R7", desc, Satisfied, "the probe used the spec's /benzene/* default paths and R3-R6 all checked out there")
	}
	return req("R7", desc, NotSatisfied, fmt.Sprintf("the probe used the spec's /benzene/* default paths, but %s did not check out there", strings.Join(failing, ", ")))
}

// evaluateR8 is never observable by a single-service black-box probe; always Inconclusive, with an
// optional labeled non-breakage bonus note when the traceparent probe was sent.
func evaluateR8(cfg config, invoke, mesh probeOutcome) Requirement {
	const desc = "Trace context join and propagation"
	const base = "trace context propagation cannot be verified by a single-service black-box HTTP probe; proving it requires either a second service to observe forwarded traceparent headers, or a mesh collector deriving consumer edges from trace parentage (mesh.md §3-4)"
	if !cfg.sendTraceParent {
		return req("R8", desc, Inconclusive, base)
	}
	if invoke.req.Verdict == Satisfied && mesh.req.Verdict == Satisfied {
		return req("R8", desc, Inconclusive, base+" (weak bonus signal: the service still responded correctly with a synthetic traceparent header attached to the R4/R6 calls - this is non-breakage only, not proof of propagation, and does not upgrade this verdict)")
	}
	return req("R8", desc, Inconclusive, base+" (a bonus traceparent header was attached to the R4/R6 calls, but at least one of those calls did not succeed, so even the weak non-breakage signal is absent)")
}

func req(id, description string, verdict Verdict, reason string) Requirement {
	return Requirement{ID: id, Description: description, Verdict: verdict, Reason: reason}
}

// get performs a GET, returning whether the service was reached, the status, the body, and a failure
// reason when it was not reached. A transport error (network, timeout, cancellation) is reached=false.
func get(ctx context.Context, client *http.Client, url string) (reached bool, status int, body, failure string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0, "", err.Error()
	}
	return do(client, request)
}

// post performs a POST of a JSON body, optionally attaching a synthetic W3C traceparent header (the
// R8 non-breakage probe).
func post(ctx context.Context, client *http.Client, url, jsonBody string, includeTraceParent bool) (reached bool, status int, body, failure string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(jsonBody))
	if err != nil {
		return false, 0, "", err.Error()
	}
	request.Header.Set("Content-Type", "application/json")
	if includeTraceParent {
		request.Header.Set("traceparent", syntheticTraceParent())
	}
	return do(client, request)
}

func do(client *http.Client, request *http.Request) (reached bool, status int, body, failure string) {
	response, err := client.Do(request)
	if err != nil {
		return false, 0, "", err.Error()
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return false, 0, "", err.Error()
	}
	return true, response.StatusCode, string(data), ""
}

// syntheticTraceParent returns a syntactically-valid, random W3C traceparent (version 00, sampled)
// for the R8 non-breakage probe.
func syntheticTraceParent() string {
	var traceID [16]byte
	var spanID [8]byte
	// crypto/rand.Read never returns an error on the platforms this runs on; if it somehow did, a
	// zero id is still a syntactically-valid traceparent for a non-breakage probe.
	_, _ = rand.Read(traceID[:])
	_, _ = rand.Read(spanID[:])
	return "00-" + hex.EncodeToString(traceID[:]) + "-" + hex.EncodeToString(spanID[:]) + "-01"
}

// hasBoolField reports whether raw parses as a JSON object with a genuinely boolean field of the
// given name. It unmarshals into `any` and type-asserts bool rather than into a bool directly,
// because json.Unmarshal of a JSON null into a bool is a no-op that returns nil - so a null-valued
// field would otherwise be mistaken for a present boolean (wire-contracts.md §5 wants a real bool).
func hasBoolField(raw, field string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return false
	}
	value, ok := obj[field]
	if !ok {
		return false
	}
	// value came from a successfully-parsed object, so it is always valid JSON; a null / string /
	// number simply leaves parsed non-bool, and the type assertion is the real test.
	var parsed any
	_ = json.Unmarshal(value, &parsed)
	_, isBool := parsed.(bool)
	return isBool
}

// isNonEmptyObject reports whether raw parses as a JSON object with at least one field.
func isNonEmptyObject(raw string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return false
	}
	return len(obj) > 0
}

// parseEnvelope validates the {statusCode: string, headers: object, body: string} response envelope
// shape (wire-contracts.md §1.2) and returns the pre-serialized inner body string.
func parseEnvelope(raw string) (innerBody, reason string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", "not valid JSON: " + err.Error(), false
	}
	var statusCode string
	if err := jsonField(obj, "statusCode", &statusCode); err != nil {
		return "", "missing a string 'statusCode' field", false
	}
	var headers map[string]json.RawMessage
	if err := jsonField(obj, "headers", &headers); err != nil {
		return "", "missing an object 'headers' field", false
	}
	var body string
	if err := jsonField(obj, "body", &body); err != nil {
		return "", "missing a string 'body' field", false
	}
	return body, "", true
}

// parseDescriptor parses an envelope's inner body as a ServiceDescriptor (mesh.md §2), requiring a
// non-empty "service" field, and returns the topic count for R2.
func parseDescriptor(innerBody string) (service string, topicCount int, reason string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(innerBody), &obj); err != nil {
		return "", 0, "envelope body is not valid JSON: " + err.Error(), false
	}
	if err := jsonField(obj, "service", &service); err != nil || service == "" {
		return "", 0, "missing a non-empty string 'service' field", false
	}
	// topics is optional-shaped for counting: absent or null is zero, not a parse failure.
	var topics []json.RawMessage
	if raw, present := obj["topics"]; present {
		_ = json.Unmarshal(raw, &topics)
	}
	return service, len(topics), "", true
}

// jsonField unmarshals a present, non-null field into target, erroring if the field is absent, JSON
// null, or the wrong type. The explicit null guard matters because json.Unmarshal of a JSON null is
// a no-op that returns nil (leaving target at its zero value) - so without it a null-valued
// statusCode/headers/body would be mistaken for a present, correctly-typed field.
func jsonField(obj map[string]json.RawMessage, name string, target any) error {
	raw, ok := obj[name]
	if !ok {
		return fmt.Errorf("missing field %q", name)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return fmt.Errorf("field %q is null", name)
	}
	return json.Unmarshal(raw, target)
}
