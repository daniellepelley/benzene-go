package cloudservice

import (
	"fmt"
	"strings"

	"github.com/daniellepelley/benzene-go/contract"
)

// Requirement is one requirement - a Cloud Service Profile requirement (R1-R5, R7 of
// docs/specification/cloud-service-profile.md §2) or a meshed-service requirement (M1, M2 of
// docs/specification/mesh.md §7.1) - and whether New's wiring provides it.
type Requirement struct {
	ID        string // "R1".."R5", "R7"; or "M1", "M2" in the Mesh report
	Name      string
	Satisfied bool
	Detail    string
}

// ProfileReport is the wiring self-check: which of the Cloud Service Profile's requirements (R1-R5,
// R7) New actually wired, and - separately, in Mesh - how far the wiring gets towards the
// meshed-service claim (M1, M2). It is derived from the builder configuration, not from probing a
// running service (that is cloudserviceprobe's job, from outside) - so it answers "how far did this
// builder get me?", cheaply, at startup, and honestly.
//
// New wires the whole profile: the synchronous HTTP surface and the derived Contract Document on
// benzene:spec (R1-R5, R7), so Satisfied() is true for a New-only assembly unless WithoutDescriptor
// takes R5 away. New carries the profile half on the Contract Document as its profile field
// (Profile); M1 and M2 are never listed there - they are not profile requirements.
//
// Being meshed is a separate claim (mesh.md §7.1): the profile plus M1 (registration, heartbeats,
// traces to a collector) and M2 (trace-context propagation). New deliberately wires neither - they
// need a collector, a push-exporter lifecycle and outbound client decorators the app owns - so
// Mesh.Satisfied() is false for a New-only build and Mesh.Unsatisfied() is the to-do list.
type ProfileReport struct {
	// Requirements holds the Cloud Service Profile requirements, R1-R5 and R7, in order.
	Requirements []Requirement
	// Mesh is the meshed-service claim's requirements, M1 and M2, reported apart from the profile.
	Mesh MeshedReport
}

// MeshedReport is the meshed-service half of the wiring self-check: M1 (service-side feeds) and M2
// (trace-context propagation) of mesh.md §7.1, formerly the profile's R6 and R8.
type MeshedReport struct {
	// Requirements holds M1 and M2, in order.
	Requirements []Requirement
}

// Satisfied reports whether every profile requirement (R1-R5, R7) is wired. It says nothing about
// the meshed-service claim - see Meshed and Mesh for that.
func (r ProfileReport) Satisfied() bool { return allSatisfied(r.Requirements) }

// Unsatisfied lists the profile requirements (R1-R5, R7) not wired - the to-do list for profile
// conformance, and exactly the Contract Document's profile.missing. Empty means the wiring covers
// the whole profile. M1 and M2 are never listed here; see Mesh.Unsatisfied.
func (r ProfileReport) Unsatisfied() []Requirement { return unsatisfied(r.Requirements) }

// Meshed reports whether the wiring makes the meshed-service claim: the whole profile plus M1 and
// M2 (mesh.md §7.1). Always false for a New-only build.
func (r ProfileReport) Meshed() bool { return r.Satisfied() && r.Mesh.Satisfied() }

// Satisfied reports whether M1 and M2 are both wired.
func (m MeshedReport) Satisfied() bool { return allSatisfied(m.Requirements) }

// Unsatisfied lists the meshed-service requirements (M1, M2) not wired.
func (m MeshedReport) Unsatisfied() []Requirement { return unsatisfied(m.Requirements) }

// ProfileName is the Cloud Service Profile's name in a Contract Document's profile field.
const ProfileName = "cloud-service"

// Profile returns the report as the Contract Document's profile self-assessment
// (contract-document.md §1, cloud-service-profile.md §5): the profile's name and the ids of the
// profile requirements (R1-R5, R7) not wired, Missing omitted when none. The meshed-service
// requirements are not part of it.
func (r ProfileReport) Profile() *contract.Profile {
	profile := &contract.Profile{Name: ProfileName}
	for _, req := range r.Unsatisfied() {
		profile.Missing = append(profile.Missing, req.ID)
	}
	return profile
}

// String renders a one-line-per-requirement summary for logging at startup: the profile, then the
// meshed-service claim.
func (r ProfileReport) String() string {
	var b strings.Builder
	writeSection(&b, "Cloud Service Profile", r.Requirements)
	writeSection(&b, "Meshed service (mesh §7.1)", r.Mesh.Requirements)
	return b.String()
}

func writeSection(b *strings.Builder, title string, reqs []Requirement) {
	satisfied := 0
	for _, req := range reqs {
		if req.Satisfied {
			satisfied++
		}
	}
	fmt.Fprintf(b, "%s: %d/%d requirements wired by this builder\n", title, satisfied, len(reqs))
	for _, req := range reqs {
		mark := "x"
		if req.Satisfied {
			mark = "ok"
		}
		fmt.Fprintf(b, "  [%2s] %-3s %-28s - %s\n", mark, req.ID, req.Name, req.Detail)
	}
}

func allSatisfied(reqs []Requirement) bool {
	for _, req := range reqs {
		if !req.Satisfied {
			return false
		}
	}
	return true
}

func unsatisfied(reqs []Requirement) []Requirement {
	var out []Requirement
	for _, req := range reqs {
		if !req.Satisfied {
			out = append(out, req)
		}
	}
	return out
}

// buildReport evaluates R1-R5 and R7, and the meshed-service M1 and M2, against the builder
// configuration.
func buildReport(cfg config) ProfileReport {
	specDetail := "registry-derived Contract Document on benzene:spec (every transport), served at /benzene/spec through the pipeline"
	if !cfg.descriptor {
		specDetail = "disabled by WithoutDescriptor (profile §4 exposure control)"
	}

	return ProfileReport{
		Requirements: []Requirement{
			{ID: "R1", Name: "hosted middleware pipeline", Satisfied: true, Detail: "traffic runs through the pipeline behind the HTTP binding"},
			{ID: "R2", Name: "message handlers via registry", Satisfied: true, Detail: "application topics served by RouterMiddleware over the registry"},
			{ID: "R3", Name: "health checks", Satisfied: true, Detail: fmt.Sprintf("%d health check(s) on benzene:healthcheck + /benzene/health", len(cfg.checks))},
			{ID: "R4", Name: "wire-envelope invocability", Satisfied: true, Detail: "envelope endpoint at /benzene/invoke"},
			{ID: "R5", Name: "derived spec", Satisfied: cfg.descriptor, Detail: specDetail},
			{ID: "R7", Name: "default service-standard paths", Satisfied: true, Detail: "reserved endpoints use the /benzene/* defaults"},
		},
		Mesh: MeshedReport{Requirements: []Requirement{
			{ID: "M1", Name: "mesh service-side feeds", Satisfied: false, Detail: "not wired by this builder - run a mesh.Announcer over Service.Contract (register + heartbeats) and mesh.TraceMiddleware with a PushExporter (traces)"},
			{ID: "M2", Name: "trace-context propagation", Satisfied: false, Detail: "not wired by this builder - add mesh.TraceMiddleware (inbound join) and the client WithTraceContext (outbound forward)"},
		}},
	}
}
