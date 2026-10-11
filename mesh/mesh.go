// Package mesh implements the service side of Benzene Mesh (the main repo's
// docs/specification/mesh.md): registering the service's Contract Document with a collector and
// heartbeating (Announcer, announce.go), a trace middleware (trace.go) that turns every pipeline
// invocation into a semantic TraceEvent handed to an Exporter - either the zero-setup LogExporter
// (exporter.go) or the batching PushExporter (push.go) that feeds a collector over the
// benzene:mesh:* wire topics (wire.go) - with span propagation for cross-service trace joins
// (span.go), and the issue feed (issues.go).
//
// The mesh has no self-description of its own (mesh.md §1): a service describes itself once, with
// the Contract Document the contract package derives and serves on benzene:spec, and that document
// is the register body (§2). The meshd package implements the collector side, where the registered
// document - not trace parentage - is the producer/consumer graph's sole source (§4); traces there
// are an observed, additive signal for liveness and drift (§4.2), never for graph membership.
//
// Every feed this package provides is independent and optional, and unavailability degrades the
// mesh rather than the service (§6). A deployment that provisions only the trace feed still yields a
// reduced mesh (live stats and flows, no catalog entries), and a registration-only deployment yields
// the reverse. Concretely: a nil Sender yields a nil Announcer or PushExporter whose methods are
// no-ops; TraceMiddleware with a nil Exporter is a pass-through; an unreachable collector leaves the
// Announcer retrying; and a panicking or failing exporter never affects the
// invocation it observed.
package mesh

import "github.com/daniellepelley/benzene-go/contract"

// Placement is where a service runs (contract-document.md §1), as DetectPlacement reports it.
type Placement = contract.Placement

// ServiceInfo is the static identity TraceMiddleware and IssueMiddleware stamp on what they emit.
// Every field is optional; zero values simply leave the corresponding fields empty.
type ServiceInfo struct {
	Service    string
	InstanceID string
}
