// Package mesh implements Phase 1 of the Benzene Mesh design (docs/design/mesh.md §8):
// a service's self-description (Descriptor) derived from its live Registry, a
// reserved-topic interception middleware that serves that descriptor, and a trace
// middleware (trace.go) that turns every pipeline invocation into a semantic TraceEvent
// handed to an Exporter. Schema derivation, descriptorHash, the push exporter, and the
// meshd collector are later phases.
//
// Every feed this package provides is independent and optional, and unavailability
// degrades the mesh rather than the service. A deployment that provisions only the trace
// feed - for example, when the descriptor endpoint is withheld pending a security review -
// still yields a reduced mesh (live stats and flows, no catalog entries), and a
// descriptor-only deployment yields the reverse. Concretely: Describe with a nil Registry
// returns a descriptor without topics and records the missing feed in Degraded;
// TraceMiddleware with a nil Exporter is a pass-through; and a panicking or failing
// exporter never affects the invocation it observed.
package mesh

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// TopicID is the reserved topic intercepted by Middleware (mesh.md §9 tracks the naming
// question; this Phase 1 uses the bare reserved id, matching the healthcheck precedent).
const TopicID = "mesh"

// FeedRegistry names the topic-catalog feed in Descriptor.Degraded: the Registry the
// descriptor's topic list is derived from.
const FeedRegistry = "registry"

// Placement locates a service instance (mesh.md §4.3). Cloud is one of "aws", "azure",
// "gcp", or "self-hosted" when detected; an explicit ServiceInfo.Placement override may
// carry any value.
type Placement struct {
	Cloud  string `json:"cloud"`
	Region string `json:"region,omitempty"`
}

// TopicDescriptor is one registered topic in a Descriptor (mesh.md §5.1). Request/response
// schemas are Phase 2 and deliberately absent here.
type TopicDescriptor struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// Descriptor is the service self-description of mesh.md §5.1: identity, placement, and
// the topic catalog derived from the Registry. It is what makes the mesh's catalog
// "derived, not declared" - there is no hand-maintained counterpart to go stale.
type Descriptor struct {
	Service        string            `json:"service"`
	ServiceVersion string            `json:"serviceVersion,omitempty"`
	InstanceID     string            `json:"instanceId,omitempty"`
	Runtime        string            `json:"runtime"`
	Binding        string            `json:"binding,omitempty"`
	Placement      Placement         `json:"placement"`
	Topics         []TopicDescriptor `json:"topics"`
	// Degraded lists the feeds that were unavailable when the descriptor was built (e.g.
	// FeedRegistry when Describe was given a nil Registry), so a reduced mesh is visible
	// as reduced rather than mistaken for a service with no topics.
	Degraded []string `json:"degraded,omitempty"`
}

// ServiceInfo is the static identity a service supplies to Describe and TraceMiddleware.
// Every field is optional; zero values simply leave the corresponding descriptor/trace
// fields empty. Placement, when its Cloud is non-empty, overrides detection wholesale -
// otherwise DetectPlacement runs.
type ServiceInfo struct {
	Service        string
	ServiceVersion string
	InstanceID     string
	Binding        string
	Placement      Placement
}

// Describe builds the service Descriptor from the live registry plus info. Call it after
// all Register calls (registration is a startup activity, so the topic list is complete
// and static from then on). A nil registry is not an error: the descriptor is built
// without a topic list and the missing feed is recorded in Degraded, so a service whose
// registry feed is deliberately not wired up still participates in the mesh reduced,
// rather than not at all.
func Describe(registry *benzene.Registry, info ServiceInfo) Descriptor {
	desc := Descriptor{
		Service:        info.Service,
		ServiceVersion: info.ServiceVersion,
		InstanceID:     info.InstanceID,
		Runtime:        "go",
		Binding:        info.Binding,
		Placement:      info.Placement,
		Topics:         []TopicDescriptor{},
	}
	if desc.Placement.Cloud == "" {
		desc.Placement = DetectPlacement()
	}
	if registry == nil {
		desc.Degraded = append(desc.Degraded, FeedRegistry)
		return desc
	}
	for _, topic := range registry.Topics() {
		desc.Topics = append(desc.Topics, TopicDescriptor{ID: topic.ID, Version: topic.Version})
	}
	return desc
}

// Middleware intercepts the reserved "mesh" topic (plus any additional aliases) and
// short-circuits the pipeline with descriptor, exactly as the healthcheck package does for
// its reserved topic. Interception is by topic ID alone, ignoring version, matching
// healthcheck's behavior. Any other topic passes through to next unchanged.
//
// Registering this middleware is what "provisions the descriptor endpoint" - a deployment
// that must not expose it (e.g. pending security review) simply leaves it out of the
// Pipeline, and the trace feed keeps working independently.
func Middleware(descriptor Descriptor, aliases ...string) benzene.Middleware {
	topics := make(map[string]bool, len(aliases)+1)
	topics[TopicID] = true
	for _, alias := range aliases {
		topics[alias] = true
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		if !topics[ic.Topic.ID] {
			return next(ctx)
		}
		ic.Result = benzene.Ok(descriptor)
		return nil
	}
}
