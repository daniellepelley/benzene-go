// Package contract is the service's self-description: the Contract Document of the main repo's
// docs/specification/contract-document.md, derived at start-up from the live Registry (the topics
// this service serves) and the OutboundRegistry (the events it publishes and the messages it sends
// to topics others define), hashed two ways (contractHash, registrationHash), and served on the
// reserved benzene:spec topic by Middleware.
//
// It is the one document every language's client generator reads, the document the Cloud Service
// Profile's R5 serves at GET /benzene/spec (httpbinding.SpecHandler, which routes the request through
// the pipeline like every other topic), and the body a mesh-enabled service registers with a
// collector (mesh.md §2; see the mesh package's Register). One derivation serves all three.
//
// Typical wiring:
//
//	outbound := contract.NewOutboundRegistry()
//	contract.MustRegisterEvent[OrderPlaced](outbound, benzene.NewTopic("order:placed"))
//	contract.MustRegisterSend[TakePayment](outbound, benzene.NewTopic("payment:take"))
//
//	doc, err := contract.Derive(registry, outbound, contract.Info{Service: "orders"})
//	if err != nil {
//		return err // a component-name collision fails here, before any message is handled
//	}
//	pipeline := benzene.NewPipeline(contract.Middleware(doc), benzene.RouterMiddleware(registry))
//
// Derivation is the one place this package uses reflect, once at start-up and never on the
// dispatch path.
package contract

import (
	"encoding/json"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
)

// TopicID is the reserved topic Middleware answers with the Contract Document
// (contract-document.md §7, wire-contracts §7).
const TopicID = "benzene:spec"

// SchemaDialect is the dialect every schema in a Document is written in: OpenAPI 3.0 Schema
// Objects. A consumer must reject a document whose dialect it does not know.
const SchemaDialect = "openapi-3.0"

// ReservedPrefix starts every reserved Benzene topic. A producer never lists such a topic in a
// document, and a consumer ignores any entry that carries one (contract-document.md §1).
const ReservedPrefix = "benzene:"

// RefPrefix is where every $ref in a Document points: a component of components.schemas.
const RefPrefix = "#/components/schemas/"

// Runtime is the value Derive puts in Document.Runtime: the implementing port.
const Runtime = "go"

// The document members Degraded can name (contract-document.md §1): a member the producer could not
// determine.
const (
	MemberRequests = "requests"
	MemberEvents   = "events"
	MemberSends    = "sends"
)

// IsReserved reports whether topic is a reserved Benzene topic (it starts with ReservedPrefix).
func IsReserved(topic string) bool { return strings.HasPrefix(topic, ReservedPrefix) }

// Schema is an OpenAPI 3.0 Schema Object held as generic JSON.
type Schema = map[string]any

// Document is the Contract Document (contract-document.md §1). Derive builds one from a service's
// registrations; it marshals to the wire shape with encoding/json.
//
// Runtime, Placement, Profile and Degraded are the deployment-side fields: outside both hashes, so
// identity and placement never change them. Degraded names a member (MemberEvents, ...) the producer
// could not determine; that member is still present and empty, and asserts nothing.
type Document struct {
	SchemaDialect    string     `json:"schemaDialect"`
	Service          string     `json:"service"`
	ServiceVersion   string     `json:"serviceVersion,omitempty"`
	VersionScheme    string     `json:"versionScheme,omitempty"`
	Description      string     `json:"description,omitempty"`
	Runtime          string     `json:"runtime,omitempty"`
	Placement        *Placement `json:"placement,omitempty"`
	Profile          *Profile   `json:"profile,omitempty"`
	Degraded         []string   `json:"degraded,omitempty"`
	MessageEndpoint  string     `json:"messageEndpoint,omitempty"`
	Transports       []string   `json:"transports,omitempty"`
	ContractHash     string     `json:"contractHash"`
	RegistrationHash string     `json:"registrationHash"`
	Requests         []Request  `json:"requests"`
	Events           []Event    `json:"events"`
	Sends            []Send     `json:"sends"`
	Components       Components `json:"components"`
}

// Request is one topic this service serves (contract-document.md §2). A nil Response marshals as
// an absent member - the handler returns no payload - while an empty, non-nil one marshals as {}:
// a payload of any shape.
type Request struct {
	Topic        string        `json:"topic"`
	Version      string        `json:"version,omitempty"`
	HTTPMappings []HTTPMapping `json:"httpMappings,omitempty"`
	Request      Schema        `json:"request"`
	Response     Schema        `json:"response,omitzero"`
	Example      any           `json:"example,omitempty"`
}

// Event is one event this service publishes and defines (contract-document.md §3.1).
type Event struct {
	Topic   string `json:"topic"`
	Version string `json:"version,omitempty"`
	Message Schema `json:"message"`
	Example any    `json:"example,omitempty"`
}

// Send is one message this service sends to a topic another party defines
// (contract-document.md §3.2). A nil Response marshals as absent: the send is one-way.
type Send struct {
	Topic    string `json:"topic"`
	Version  string `json:"version,omitempty"`
	Request  Schema `json:"request"`
	Response Schema `json:"response,omitzero"`
}

// Placement is where a service runs (contract-document.md §1). Cloud is "aws", "azure", "gcp",
// "self-hosted" or an explicit override; Region is set only where the platform documents a way to
// know it - a producer never guesses one.
type Placement struct {
	Cloud  string `json:"cloud"`
	Region string `json:"region,omitempty"`
}

// Profile is a conformance-profile self-assessment (contract-document.md §1): Name is the profile
// ("cloud-service" for the Cloud Service Profile) and Missing the requirement ids the service's own
// wiring knows it does not meet - for cloud-service, ids from R1-R5 and R7 only
// (cloud-service-profile.md §5), omitted when none. It reflects wiring, not runtime health.
type Profile struct {
	Name    string   `json:"name"`
	Missing []string `json:"missing,omitempty"`
}

// HTTPMapping is one HTTP route a topic is reachable on.
type HTTPMapping struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Components holds the named schemas every $ref points into.
type Components struct {
	Schemas map[string]Schema `json:"schemas"`
}

// Hashes computes the document's contractHash and registrationHash (contract-document.md §6) from
// its own entries, whatever its ContractHash and RegistrationHash fields currently hold.
func (d Document) Hashes() (contractHash, registrationHash string, err error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", "", err
	}
	return HashDocument(raw)
}

// Info is the identity and transport facts Derive puts in the document beside what it derives.
// Service is required.
type Info struct {
	// Service is the service's name (required).
	Service string
	// ServiceVersion is which release this is; VersionScheme is its comparison scheme, required
	// for an orderable version (service-versions.md §2).
	ServiceVersion string
	VersionScheme  string
	// Description is free text.
	Description string
	// MessageEndpoint is the path of the envelope endpoint over HTTP (httpbinding.EnvelopePath),
	// or empty when the service has none: a consumer feature-detects send capability on it.
	MessageEndpoint string
	// Transports are the transports the service receives messages over ("http", "sqs", ...).
	Transports []string
	// Placement is where the service runs, or nil to leave the document without one. Derive does not
	// detect it; mesh.DetectPlacement reads the platform's documented environment.
	Placement *Placement
	// Profile is the conformance-profile self-assessment, or nil (cloudservice.New sets it).
	Profile *Profile
	// HTTPRoutes are the service's HTTP routes, attached to the requests they reach as
	// httpMappings. A route whose topic has no version reaches every version of its topic id (the
	// version then travels in a header or a {version} path segment); a route to a reserved or
	// unregistered topic is ignored.
	HTTPRoutes []HTTPRoute
}

// HTTPRoute is one HTTP route: a method and path that reach a topic. It mirrors httpbinding.Route,
// which this package cannot import.
type HTTPRoute struct {
	Method string
	Path   string
	Topic  benzene.Topic
}
