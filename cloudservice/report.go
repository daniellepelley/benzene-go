package cloudservice

import (
	"fmt"
	"strings"
)

// SurfaceKind classifies how load-bearing a profile surface is to conformance.
type SurfaceKind int

const (
	// Required surfaces must be present for a service to be a Cloud Service at all; New always
	// provides them, so Satisfied reflects only whether the operator opted out of anything.
	Required SurfaceKind = iota
	// Recommended surfaces are on by default but can be deliberately disabled (e.g. the descriptor,
	// via WithoutDescriptor, per profile §4 exposure control).
	Recommended
	// Informational surfaces are reported for visibility but never counted against conformance (e.g.
	// the outbound mesh feeds this builder does not own).
	Informational
)

func (k SurfaceKind) String() string {
	switch k {
	case Required:
		return "required"
	case Recommended:
		return "recommended"
	default:
		return "informational"
	}
}

// Surface is one profile surface and whether the wiring provides it.
type Surface struct {
	Name      string
	Kind      SurfaceKind
	Satisfied bool
	Detail    string
}

// ProfileReport is the wiring self-check: which Cloud Service Profile surfaces New actually mounted.
// It is derived from the builder configuration, not from probing a running service (that is
// cloudserviceprobe's job, from outside) - so it answers "did I wire this correctly?", cheaply, at
// startup.
type ProfileReport struct {
	Surfaces []Surface
}

// Satisfied reports whether every Required surface is present. Recommended surfaces a deployment
// deliberately disabled do not make it false; use Unsatisfied to see those.
func (r ProfileReport) Satisfied() bool {
	for _, s := range r.Surfaces {
		if s.Kind == Required && !s.Satisfied {
			return false
		}
	}
	return true
}

// Unsatisfied lists the Required and Recommended surfaces that are not present (Informational
// surfaces are never included). Empty means the wiring is fully conformant.
func (r ProfileReport) Unsatisfied() []Surface {
	var out []Surface
	for _, s := range r.Surfaces {
		if s.Kind != Informational && !s.Satisfied {
			out = append(out, s)
		}
	}
	return out
}

// String renders a one-line-per-surface summary for logging at startup.
func (r ProfileReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cloud Service Profile wiring: %s\n", satisfiedLabel(r.Satisfied()))
	for _, s := range r.Surfaces {
		mark := "x"
		if s.Satisfied {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%s] %-20s (%s) - %s\n", mark, s.Name, s.Kind, s.Detail)
	}
	return b.String()
}

func satisfiedLabel(ok bool) string {
	if ok {
		return "all required surfaces present"
	}
	return "MISSING required surfaces"
}

// buildReport evaluates the profile surfaces from the builder configuration.
func buildReport(cfg config) ProfileReport {
	descriptorDetail := "reserved benzene:mesh descriptor topic mounted (R6, inbound)"
	specDetail := "registry-derived descriptor served at /benzene/spec (R5)"
	if !cfg.descriptor {
		descriptorDetail = "disabled by WithoutDescriptor (profile §4 exposure control)"
		specDetail = "disabled by WithoutDescriptor (profile §4 exposure control)"
	}

	return ProfileReport{Surfaces: []Surface{
		{Name: "envelope-invoke", Kind: Required, Satisfied: true, Detail: "wire-envelope entry point mounted at /benzene/invoke (R2)"},
		{Name: "health", Kind: Required, Satisfied: true, Detail: fmt.Sprintf("%d health check(s) on the reserved topic + /benzene/health (R3)", len(cfg.checks))},
		{Name: "default-paths", Kind: Required, Satisfied: true, Detail: "reserved endpoints use the /benzene/* default standard (R1/R7)"},
		{Name: "mesh-descriptor", Kind: Recommended, Satisfied: cfg.descriptor, Detail: descriptorDetail},
		{Name: "derived-spec", Kind: Recommended, Satisfied: cfg.descriptor, Detail: specDetail},
		{Name: "mesh-outbound-feeds", Kind: Informational, Satisfied: false, Detail: "register/heartbeat/traces to a collector are not wired by this builder - drive them with the mesh push exporters"},
	}}
}
