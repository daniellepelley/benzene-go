// Package cloudserviceprobe is the external, black-box conformance checker for the Benzene Cloud
// Service Profile (docs/specification/cloud-service-profile.md §2, §5). It hits a running service
// over real HTTP from the outside and independently verifies the profile's requirements - R1-R5 and
// R7 - exactly as an operator or a CI job auditing an arbitrary service would, .NET, Go, or
// otherwise. It reports what it can observe of the separate meshed-service claim (mesh.md §7.1: M1
// feeds, M2 trace propagation) apart from the profile. It is the Go port of
// Benzene.CloudService.Probe.
//
// It is deliberately self-contained and depends on nothing else in this module: the profile is a
// language-neutral spec, so this tool must be usable against ANY Benzene Cloud Service reachable
// over HTTP - it therefore parses the wire shapes it checks (wire-contracts.md §1.2/§5,
// contract-document.md §1)
// independently rather than importing this port's own models, and keeps its own copy of the
// /benzene/* default path constants rather than referencing httpbinding. Do not "fix" that into a
// shared reference; the independence is the point (it is what lets the same tool audit a non-Go
// service).
//
// Honesty rule: the result is tri-state (Satisfied / NotSatisfied / Inconclusive), never a bool. R2
// is inferential and R7 becomes unknowable the moment the caller points the probe at non-default
// paths; those stay Inconclusive when the evidence is not there. The meshed-service M1 (register,
// heartbeat and trace delivery to a collector) and M2 (trace propagation) are not observable from one
// service in isolation, so they are always Inconclusive, never silently upgraded to Satisfied. Run
// never returns an error for an unreachable or non-conformant service: unreachability and shape
// mismatches are verdicts, not failures.
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

// The default service standard's well-known HTTP paths (transport-bindings.md §1, mandatory by
// default under the profile's R7). These deliberately duplicate httpbinding's constants rather than
// referencing them, so this package can audit any service (including non-Go ports) without a
// dependency on this port's own wiring - see the package doc.
const (
	// InvokePath is the wire-envelope endpoint (profile R4, and R5's benzene:spec topic half).
	InvokePath = "/benzene/invoke"
	// SpecPath is the derived spec document endpoint (profile R5's HTTP half).
	SpecPath = "/benzene/spec"
	// HealthPath is the aggregated health check endpoint (profile R3).
	HealthPath = "/benzene/health"
)

// The synthetic envelopes the probe POSTs to the invoke endpoint: a healthcheck round-trip for R4,
// and the reserved benzene:spec topic - which R5 requires a service to answer with its contract
// document wherever the transport carries a reply, as the envelope endpoint does - for R5's topic
// half. Both use the reserved benzene: topics.
const (
	healthcheckEnvelope = `{"topic":"benzene:healthcheck","headers":{},"body":"{}"}`
	specEnvelope        = `{"topic":"benzene:spec","headers":{},"body":"{}"}`
)

// Verdict is the tri-state assessment an external probe can honestly reach for one requirement.
type Verdict string

const (
	// Satisfied means the probe independently observed evidence that the requirement holds.
	Satisfied Verdict = "satisfied"
	// NotSatisfied means the probe reached the service and observed that the requirement does not hold.
	NotSatisfied Verdict = "not-satisfied"
	// Inconclusive means the probe cannot determine the requirement from outside the service alone -
	// the evidence was unreachable (cascading from another failure), or the requirement is inherently
	// unobservable by a single-service HTTP probe (the meshed-service M1 and M2). Never treat it as a
	// pass.
	Inconclusive Verdict = "inconclusive"
)

// Requirement is one requirement as independently assessed by a probe run.
type Requirement struct {
	// ID is the requirement id: R1-R5 or R7 for the profile, M1 or M2 for the meshed-service claim.
	ID string
	// Description is what the requirement mandates, in one line.
	Description string
	// Verdict is the probe's independently-observed verdict.
	Verdict Verdict
	// Reason is why the verdict is what it is - always populated, since an outside observer explaining
	// itself matters even more when it has no service-side word to fall back on.
	Reason string
}

// Report is the result of one probe run: an external, black-box, tri-state assessment of the Cloud
// Service Profile (R1-R5, R7), with the meshed-service claim (M1, M2) reported apart, built entirely
// from what the probe observed over HTTP. It never trusts anything the service claims about itself
// (e.g. the profile field on its own contract document).
type Report struct {
	// Requirements holds every profile requirement with its assessment, in order: R1-R5, R7.
	Requirements []Requirement
	// Mesh holds the meshed-service claim's requirements (mesh.md §7.1), M1 and M2. Neither is
	// observable from one service in isolation, so both are always Inconclusive here, with a reason
	// saying what the probe did and did not see - including, since a meshed service is a Cloud
	// Service first, any profile requirement observed unmet. They are not part of the profile
	// verdict.
	Mesh []Requirement
}

// NotSatisfied returns the profile requirement ids the probe positively observed as unmet.
func (r *Report) NotSatisfied() []string { return r.idsWith(NotSatisfied) }

// Inconclusive returns the profile requirement ids the probe could not determine from outside the
// service. The meshed-service M1 and M2 are not included; read Mesh for those.
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

// IsFullyConformant reports whether every profile requirement (R1-R5, R7) was observed as Satisfied.
// It says nothing about the meshed-service claim, which a single-service probe cannot observe (see
// Mesh). R2 is inferential, so a service with no registered requests reads Inconclusive there and is
// not reported fully conformant - read NotSatisfied and Inconclusive for the actual picture.
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

// WithInvokePath overrides the wire-envelope endpoint probed for R4 and R5's benzene:spec topic
// half (default InvokePath).
func WithInvokePath(path string) Option { return func(c *config) { c.invokePath = path } }

// WithSpecPath overrides the derived-spec endpoint probed for R5's HTTP half (default SpecPath).
func WithSpecPath(path string) Option { return func(c *config) { c.specPath = path } }

// WithHealthPath overrides the health endpoint probed for R3 (default HealthPath).
func WithHealthPath(path string) Option { return func(c *config) { c.healthPath = path } }

// WithoutTraceParentProbe turns off the default-on synthetic traceparent header on the envelope
// probe requests. The header is only ever a weak, explicitly-labeled non-breakage signal for the
// meshed-service M2 - it never upgrades M2 past Inconclusive - so disabling it only removes that
// note from M2's reason.
func WithoutTraceParentProbe() Option { return func(c *config) { c.sendTraceParent = false } }

// Run probes the service at baseURL and returns a tri-state assessment of the profile (R1-R5, R7),
// with what it can say of the meshed-service claim (M1, M2) apart. It never returns an error for an
// unreachable or non-conformant service - those become verdicts. A nil client uses
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
	specTopic := probeSpecTopic(ctx, client, baseURL, cfg)
	r5 := evaluateR5(spec, specTopic)

	report := &Report{
		Requirements: []Requirement{
			evaluateR1(health, invoke, spec, specTopic),
			evaluateR2(specTopic),
			health.req,
			invoke.req,
			r5,
			evaluateR7(cfg, health, invoke, r5),
		},
	}
	report.Mesh = []Requirement{
		withProfilePrerequisite(report, evaluateM1(specTopic)),
		withProfilePrerequisite(report, evaluateM2(cfg, invoke, specTopic)),
	}
	return report
}

// withProfilePrerequisite notes on a meshed-service requirement that the meshed claim requires the
// Cloud Service Profile (mesh.md §7.1) when the probe observed some of it unmet - so a reader of the
// mesh section alone is not left thinking only M1/M2 stand between the service and the claim.
func withProfilePrerequisite(report *Report, m Requirement) Requirement {
	if unmet := report.NotSatisfied(); len(unmet) > 0 {
		m.Reason += fmt.Sprintf(". The meshed claim also requires the Cloud Service Profile, and the probe observed %s unmet", strings.Join(unmet, ", "))
	}
	return m
}

// probeOutcome is the internal result of probing one surface: its verdict (as a Requirement),
// whether the surface was reached at all (for the inferential R1), and - for the benzene:spec topic
// probe - the contract document's request count.
type probeOutcome struct {
	req        Requirement
	reached    bool
	topicCount int
}

const (
	descR3 = "Health checks"
	descR4 = "Wire-envelope invocability"
	descR5 = "Derived spec (GET /benzene/spec and the benzene:spec topic)"
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

// probeSpec is R5's HTTP half: GET the spec path; satisfied iff 200 with a non-empty JSON object. No
// specific spec format (OpenAPI, AsyncAPI, or Benzene's own) is assumed - only that a real document
// is there.
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

// probeSpecTopic is R5's topic half: POST the reserved benzene:spec topic over the envelope;
// satisfied iff a 200 envelope wraps a contract document with a non-empty "service" field
// (contract-document.md §1, §7) - the document R5 requires wherever a reply exists, and the body a
// meshed service registers (mesh.md §2). The document's request count is carried out for R2's
// inferential check.
func probeSpecTopic(ctx context.Context, client *http.Client, baseURL string, cfg config) probeOutcome {
	reached, status, body, failure := post(ctx, client, baseURL+cfg.invokePath, specEnvelope, cfg.sendTraceParent)
	if !reached {
		return probeOutcome{req: req("R5", descR5, NotSatisfied, fmt.Sprintf("POST %s with topic 'benzene:spec' did not reach the service: %s", cfg.invokePath, failure))}
	}
	if status != 200 {
		return probeOutcome{req: req("R5", descR5, NotSatisfied, fmt.Sprintf("POST %s with topic 'benzene:spec' returned %d, expected 200 (the reserved self-description topic does not appear to be served)", cfg.invokePath, status)), reached: true}
	}
	inner, envReason, ok := parseEnvelope(body)
	if !ok {
		return probeOutcome{req: req("R5", descR5, NotSatisfied, fmt.Sprintf("200 response to the 'benzene:spec' topic did not have the {statusCode, headers, body} envelope shape (wire-contracts.md §1.2): %s", envReason)), reached: true}
	}
	service, topics, descReason, ok := parseContractDocument(inner)
	if !ok {
		return probeOutcome{req: req("R5", descR5, NotSatisfied, fmt.Sprintf("200 response to the 'benzene:spec' topic did not parse as a contract document with a non-empty 'service' field (contract-document.md §1): %s", descReason)), reached: true}
	}
	return probeOutcome{
		req:        req("R5", descR5, Satisfied, fmt.Sprintf("the reserved 'benzene:spec' topic served a contract document for service '%s' over the envelope at %s", service, cfg.invokePath)),
		reached:    true,
		topicCount: topics,
	}
}

// evaluateR5 combines the two halves of R5 (cloud-service-profile.md §2): the service intercepts
// the reserved benzene:spec topic and answers it with its contract document wherever the transport
// carries a reply - observable here over the envelope endpoint, which does - and, having an HTTP
// surface, serves it at the spec path. Both must hold. A one-way binding cannot answer, and the probe
// never asks it to.
func evaluateR5(spec, specTopic probeOutcome) Requirement {
	verdict := Satisfied
	if spec.req.Verdict != Satisfied || specTopic.req.Verdict != Satisfied {
		verdict = NotSatisfied
	}
	return req("R5", descR5, verdict, "HTTP: "+spec.req.Reason+"; topic: "+specTopic.req.Reason)
}

// evaluateR1 is inferential: satisfied iff anything responded at all, which proves a hosted pipeline
// exists behind a transport binding (an in-process-only pipeline would have nothing to reach).
func evaluateR1(health, invoke, spec, specTopic probeOutcome) Requirement {
	const desc = "Hosted middleware pipeline"
	if health.reached || invoke.reached || spec.reached || specTopic.reached {
		return req("R1", desc, Satisfied, "at least one probed surface responded over HTTP, which proves a hosted pipeline exists behind a transport binding - an in-process-only pipeline would have nothing for this probe to reach")
	}
	return req("R1", desc, NotSatisfied, "none of the health, invoke, spec, or benzene:spec surfaces responded at all; no hosted pipeline was reachable at the configured base address")
}

// evaluateR2 is inferential from the requests of the contract document read over the envelope on
// the benzene:spec topic - R5's GET is format-agnostic, so request counts are not read from it.
func evaluateR2(specTopic probeOutcome) Requirement {
	const desc = "Message handlers via the registry"
	if specTopic.req.Verdict != Satisfied {
		return req("R2", desc, Inconclusive, "no contract document reachable on the benzene:spec topic to read registered requests from - absence of evidence isn't evidence of absence")
	}
	if specTopic.topicCount > 0 {
		return req("R2", desc, Satisfied, fmt.Sprintf("the contract document on benzene:spec lists %d request(s) (contract-document.md §2), which can only be derived from the handler registry", specTopic.topicCount))
	}
	return req("R2", desc, Inconclusive, "the contract document is reachable but lists no requests; a service can legitimately have zero handlers registered so far - absence of evidence isn't evidence of absence")
}

// evaluateR7 is only assessable when the probe itself used the defaults; otherwise it can't tell what
// the service defaults to.
func evaluateR7(cfg config, health, invoke probeOutcome, r5 Requirement) Requirement {
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
	if r5.Verdict != Satisfied {
		failing = append(failing, "R5 (spec)")
	}
	if len(failing) == 0 {
		return req("R7", desc, Satisfied, "the probe used the spec's /benzene/* default paths and R3-R5 all checked out there")
	}
	return req("R7", desc, NotSatisfied, fmt.Sprintf("the probe used the spec's /benzene/* default paths, but %s did not check out there", strings.Join(failing, ", ")))
}

// evaluateM1 is the meshed-service M1 (mesh.md §7.1): registration, heartbeats and traces delivered
// to a collector. None of the three is observable by probing the service alone, so it is always
// Inconclusive; the reason notes whether the document the service would register is at least served.
func evaluateM1(specTopic probeOutcome) Requirement {
	const desc = "Mesh service-side feeds (meshed service)"
	const base = "registration (benzene:mesh:register), heartbeat (benzene:mesh:heartbeat) and trace (benzene:mesh:traces) delivery to a collector cannot be observed by probing the service alone; a collector's view of the service is the evidence (mesh.md §7.1)"
	if specTopic.req.Verdict == Satisfied {
		return req("M1", desc, Inconclusive, base+" (the contract document a registration would carry is served on benzene:spec, which is R5, not M1)")
	}
	return req("M1", desc, Inconclusive, base)
}

// evaluateM2 is the meshed-service M2 (mesh.md §7.1), never observable by a single-service black-box
// probe; always Inconclusive, with an optional labeled non-breakage bonus note when the traceparent
// probe was sent.
func evaluateM2(cfg config, invoke, specTopic probeOutcome) Requirement {
	const desc = "Trace context join and propagation (meshed service)"
	const base = "trace context propagation cannot be verified by a single-service black-box HTTP probe; proving it requires either a second service to observe forwarded traceparent headers, or a mesh collector's declared-vs-observed signal showing the edge as actually exercised (mesh.md §3-4.2 - the producer/consumer graph itself comes from the registered contract document, not from trace parentage)"
	if !cfg.sendTraceParent {
		return req("M2", desc, Inconclusive, base)
	}
	if invoke.req.Verdict == Satisfied && specTopic.req.Verdict == Satisfied {
		return req("M2", desc, Inconclusive, base+" (weak bonus signal: the service still responded correctly with a synthetic traceparent header attached to the envelope calls - this is non-breakage only, not proof of propagation, and does not upgrade this verdict)")
	}
	return req("M2", desc, Inconclusive, base+" (a bonus traceparent header was attached to the envelope calls, but at least one of those calls did not succeed, so even the weak non-breakage signal is absent)")
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
// M2 non-breakage probe).
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
// for the M2 non-breakage probe.
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

// parseContractDocument parses an envelope's inner body as a contract document
// (contract-document.md §1), requiring a non-empty "service" field, and returns its request count
// for R2 - reserved benzene: entries, which a producer never lists and a consumer ignores, excluded.
func parseContractDocument(innerBody string) (service string, requestCount int, reason string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(innerBody), &obj); err != nil {
		return "", 0, "envelope body is not valid JSON: " + err.Error(), false
	}
	if err := jsonField(obj, "service", &service); err != nil || service == "" {
		return "", 0, "missing a non-empty string 'service' field", false
	}
	// requests is optional-shaped for counting: absent or null is zero, not a parse failure.
	var requests []struct {
		Topic string `json:"topic"`
	}
	if raw, present := obj["requests"]; present {
		_ = json.Unmarshal(raw, &requests)
	}
	for _, entry := range requests {
		if !strings.HasPrefix(entry.Topic, "benzene:") {
			requestCount++
		}
	}
	return service, requestCount, "", true
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
