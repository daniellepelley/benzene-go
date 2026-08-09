package cloudservice

import (
	"fmt"
	"strings"
)

// Requirement is one Cloud Service Profile requirement (R1-R8 of
// docs/specification/cloud-service-profile.md §2) and whether New's wiring provides it.
type Requirement struct {
	ID        string // "R1".."R8"
	Name      string
	Satisfied bool
	Detail    string
}

// ProfileReport is the wiring self-check: which of the profile's R1-R8 requirements New actually
// wired. It is derived from the builder configuration, not from probing a running service (that is
// cloudserviceprobe's job, from outside) - so it answers "how far did this builder get me?", cheaply,
// at startup, and honestly.
//
// New wires the synchronous HTTP surface (R1-R5, R7). It deliberately does NOT wire the outbound half
// of R6 (the register/heartbeat/traces mesh feeds) or R8 (trace-context propagation): those need a
// mesh collector + push-exporter lifecycle and outbound client decorators the app owns. So for a
// New-only assembly Satisfied() is false by design, and Unsatisfied() is the exact to-do list an
// operator must complete (add the mesh feeds and trace propagation) to be fully profile-conformant.
// This mirrors .NET's CloudServiceProfileReport evaluating the wiring against all of R1-R8, rather
// than quietly reporting the HTTP surface as if it were the whole profile.
type ProfileReport struct {
	Requirements []Requirement
}

// Satisfied reports whether every profile requirement (R1-R8) is wired. It is false for a New-only
// assembly (R6's outbound feeds and R8 are not this builder's to wire) - see Unsatisfied for what
// remains.
func (r ProfileReport) Satisfied() bool {
	for _, req := range r.Requirements {
		if !req.Satisfied {
			return false
		}
	}
	return true
}

// Unsatisfied lists the requirements not wired - the to-do list for full profile conformance. Empty
// means the wiring covers all of R1-R8.
func (r ProfileReport) Unsatisfied() []Requirement {
	var out []Requirement
	for _, req := range r.Requirements {
		if !req.Satisfied {
			out = append(out, req)
		}
	}
	return out
}

// String renders a one-line-per-requirement summary for logging at startup.
func (r ProfileReport) String() string {
	satisfied := 0
	for _, req := range r.Requirements {
		if req.Satisfied {
			satisfied++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Cloud Service Profile: %d/%d requirements wired by this builder\n", satisfied, len(r.Requirements))
	for _, req := range r.Requirements {
		mark := "x"
		if req.Satisfied {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%2s] %-3s %-28s - %s\n", mark, req.ID, req.Name, req.Detail)
	}
	return b.String()
}

// buildReport evaluates R1-R8 against the builder configuration.
func buildReport(cfg config) ProfileReport {
	specDetail := "registry-derived descriptor served at /benzene/spec"
	if !cfg.descriptor {
		specDetail = "disabled by WithoutDescriptor (profile §4 exposure control)"
	}

	r6Detail := "descriptor feed (benzene:mesh) wired, but register/heartbeat/traces are not wired by " +
		"this builder - drive them with the mesh push exporters + a collector"
	if !cfg.descriptor {
		r6Detail = "not wired: descriptor disabled (WithoutDescriptor), and register/heartbeat/traces " +
			"are not this builder's responsibility"
	}

	return ProfileReport{Requirements: []Requirement{
		{ID: "R1", Name: "hosted middleware pipeline", Satisfied: true, Detail: "traffic runs through the pipeline behind the HTTP binding"},
		{ID: "R2", Name: "message handlers via registry", Satisfied: true, Detail: "application topics served by RouterMiddleware over the registry"},
		{ID: "R3", Name: "health checks", Satisfied: true, Detail: fmt.Sprintf("%d health check(s) on benzene:healthcheck + /benzene/health", len(cfg.checks))},
		{ID: "R4", Name: "wire-envelope invocability", Satisfied: true, Detail: "envelope endpoint at /benzene/invoke"},
		{ID: "R5", Name: "derived spec", Satisfied: cfg.descriptor, Detail: specDetail},
		{ID: "R6", Name: "mesh service-side feeds", Satisfied: false, Detail: r6Detail},
		{ID: "R7", Name: "default service-standard paths", Satisfied: true, Detail: "reserved endpoints use the /benzene/* defaults"},
		{ID: "R8", Name: "trace-context propagation", Satisfied: false, Detail: "not wired by this builder - add mesh.TraceMiddleware (inbound join) and the client TraceContextDecorator (outbound forward)"},
	}}
}
