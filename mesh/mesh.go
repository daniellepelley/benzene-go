// Package mesh implements the Benzene Mesh design (the main repo's
// docs/specification/mesh.md, originally extracted from this package's earlier
// docs/design/mesh.md): a service's self-description (Descriptor) derived from its live
// Registry (what it provides, §2) and its live OutboundRegistry (what it consumes, §2.3) -
// including per-topic request/response JSON Schemas derived at startup from the registered
// types, and the contract hash that makes drift detectable (schema.go) - a reserved-topic
// interception middleware that serves that descriptor, and a trace middleware (trace.go) that
// turns every pipeline invocation into a semantic TraceEvent handed to an Exporter - either the
// zero-setup LogExporter (exporter.go) or the batching PushExporter (push.go) that feeds a
// collector over the mesh:* wire topics (wire.go), with span propagation for cross-service
// trace joins (span.go). The meshd package implements the collector side, where the declared
// Descriptor - not trace parentage - is the producer/consumer graph's sole source (mesh.md §4);
// traces there are an observed, additive signal for liveness and drift (§4.2), never for graph
// membership.
//
// Every feed this package provides is independent and optional, and unavailability
// degrades the mesh rather than the service. A deployment that provisions only the trace
// feed - for example, when the descriptor endpoint is withheld pending a security review -
// still yields a reduced mesh (live stats and flows, no catalog entries), and a
// descriptor-only deployment yields the reverse. Concretely: Describe with a nil Registry or a
// nil OutboundRegistry returns a descriptor without that half of the catalog and records the
// missing feed in Degraded; TraceMiddleware with a nil Exporter is a pass-through; and a
// panicking or failing exporter never affects the invocation it observed.
package mesh

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// TopicID is the reserved topic intercepted by Middleware to serve the ServiceDescriptor
// (mesh.md §1). It is namespaced under the benzene: default-service-standard prefix
// (design-principles.md §5.1) and matches the .NET reference's BenzeneTopic.Mesh.
const TopicID = "benzene:mesh"

// FeedRegistry names the topic-catalog feed in Descriptor.Degraded: the Registry the
// descriptor's topic list is derived from.
const FeedRegistry = "registry"

// FeedOutboundRegistry names the produced-topic feed in Descriptor.Degraded: the
// OutboundRegistry the descriptor's Produces list is derived from (mesh.md §2.3).
const FeedOutboundRegistry = "outbound-registry"

// Placement locates a service instance (mesh.md §4.3). Cloud is one of "aws", "azure",
// "gcp", or "self-hosted" when detected; an explicit ServiceInfo.Placement override may
// carry any value.
type Placement struct {
	Cloud  string `json:"cloud"`
	Region string `json:"region,omitempty"`
}

// TopicDescriptor is one registered topic in a Descriptor (mesh.md §5.1). The schemas
// describe the marshaled request/response forms, derived at startup from the TReq/TRes
// types captured at the Register call site (see deriveSchema for the exact mapping); they
// are what lets the mesh flag schema drift from live data instead of hand-written specs.
// The schemas are never omitted from the wire even when unconstrained ({}): a consumed topic
// with no declared response type (mesh.md §2.3) marshals responseSchema as the empty object,
// not as an absent key - a reader must be able to tell "unconstrained" from "not derived".
type TopicDescriptor struct {
	ID             string         `json:"id"`
	Version        string         `json:"version,omitempty"`
	RequestSchema  map[string]any `json:"requestSchema"`
	ResponseSchema map[string]any `json:"responseSchema"`
}

// Descriptor is the service self-description of mesh.md §2: identity, placement, the topic
// catalog derived from the Registry (what this service provides), and the consumed-topic
// catalog derived from the OutboundRegistry (what this service consumes, §2.3). It is what
// makes the mesh's catalog "derived, not declared" - there is no hand-maintained counterpart to
// go stale, on either side of the graph.
type Descriptor struct {
	Service        string            `json:"service"`
	ServiceVersion string            `json:"serviceVersion,omitempty"`
	InstanceID     string            `json:"instanceId,omitempty"`
	Runtime        string            `json:"runtime"`
	Binding        string            `json:"binding,omitempty"`
	Placement      Placement         `json:"placement"`
	Topics         []TopicDescriptor `json:"topics"`
	// Produces is every registered outbound topic (mesh.md §2.3): what this service produces.
	// This is the field the collector's PROVIDER-edge derivation reads (mesh.md §4) - a topic
	// absent here is not produced by this service, regardless of what traffic has or hasn't
	// flowed. Populated the same way Topics is: always present, empty when the service produces
	// nothing (as opposed to a nil OutboundRegistry, which instead degrades the feed).
	//
	// Named produces, and paired with Topics meaning what this service CONSUMES, since the
	// 2026-08 role inversion (mesh.md §4): registering a handler for a topic makes a service
	// that topic's consumer, which is how every broker in the field uses the word. Before that
	// this field was consumes and the two roles were the other way round.
	Produces []TopicDescriptor `json:"produces"`
	// DescriptorHash is the contract hash (mesh.md §2.2): stable across instances and
	// heartbeats of the same build, changed exactly when the contract changes - which is
	// what lets a collector detect a redeploy (or a schema change without a version bump)
	// from the hash alone. See descriptorHash for what it covers and excludes.
	DescriptorHash string `json:"descriptorHash,omitempty"`
	// Degraded lists the feeds that were unavailable when the descriptor was built (e.g.
	// FeedRegistry when Describe was given a nil Registry, FeedOutboundRegistry when given a
	// nil OutboundRegistry), so a reduced mesh is visible as reduced rather than mistaken for a
	// service that provides or consumes nothing.
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

// Describe builds the service Descriptor from the live registry, the live outbound registry,
// and info. Call it after all Register/RegisterOutbound calls (registration is a startup
// activity, so both lists are complete and static from then on). A nil registry or a nil
// outbound registry is not an error: the descriptor is built without that half of the catalog
// and the missing feed is recorded in Degraded, so a service whose registry or outbound-registry
// feed is deliberately not wired up still participates in the mesh reduced, rather than not at
// all - the same degradation rule applied symmetrically to both halves of the contract.
func Describe(registry *benzene.Registry, outbound *OutboundRegistry, info ServiceInfo) Descriptor {
	desc := Descriptor{
		Service:        info.Service,
		ServiceVersion: info.ServiceVersion,
		InstanceID:     info.InstanceID,
		Runtime:        "go",
		Binding:        info.Binding,
		Placement:      info.Placement,
		Topics:         []TopicDescriptor{},
		Produces:       []TopicDescriptor{},
	}
	if desc.Placement.Cloud == "" {
		desc.Placement = DetectPlacement()
	}
	if registry == nil {
		desc.Degraded = append(desc.Degraded, FeedRegistry)
	} else {
		for _, topic := range registry.Topics() {
			// The blank ok: Topics() and TopicTypes() read the same registrations, so a
			// topic from the former always resolves in the latter - and if a future
			// Registry change ever broke that, deriveSchema(nil) degrades the entry to
			// schema-less rather than failing, per this package's degradation rule.
			requestType, responseType, _ := registry.TopicTypes(topic)
			desc.Topics = append(desc.Topics, TopicDescriptor{
				ID:             topic.ID,
				Version:        topic.Version,
				RequestSchema:  deriveSchema(requestType),
				ResponseSchema: deriveSchema(responseType),
			})
		}
	}
	if outbound == nil {
		desc.Degraded = append(desc.Degraded, FeedOutboundRegistry)
	} else {
		for _, topic := range outbound.Topics() {
			// Same blank-ok defensive rationale as the registry loop above.
			requestType, responseType, _ := outbound.TopicTypes(topic)
			desc.Produces = append(desc.Produces, TopicDescriptor{
				ID:             topic.ID,
				Version:        topic.Version,
				RequestSchema:  deriveSchema(requestType),
				ResponseSchema: deriveSchema(responseType),
			})
		}
	}
	desc.DescriptorHash = descriptorHash(desc)
	return desc
}

// Middleware intercepts the reserved benzene:mesh topic (plus any additional aliases) and
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
