package mesh

import (
	"time"

	"github.com/daniellepelley/benzene-go/healthcheck"
)

// The mesh wire-contract topics (mesh.md §4): what a service sends to a collector
// (register/heartbeat/traces) and what a view reads back (query:*). The collector side is
// implemented by the meshd package; the names live here because both sides of the wire
// share them. They are namespaced under the benzene: default-service-standard prefix
// (design-principles.md §5.1) and match the .NET reference's MeshTopics/BenzeneTopic, so
// other language ports interoperate.
const (
	TopicRegister  = "benzene:mesh:register"
	TopicHeartbeat = "benzene:mesh:heartbeat"
	TopicTraces    = "benzene:mesh:traces"
	TopicIssues    = "benzene:mesh:issues"

	TopicQueryFleet   = "benzene:mesh:query:fleet"
	TopicQueryService = "benzene:mesh:query:service"
	TopicQueryTopic   = "benzene:mesh:query:topic"
	TopicQueryTrace   = "benzene:mesh:query:trace"
)

// TraceBatch is the body of a mesh:traces message: the events a PushExporter accumulated
// since its last flush.
type TraceBatch struct {
	Events []TraceEvent `json:"events"`
}

// IssueBatch is the body of a benzene:mesh:issues message (mesh.md §4.1): a service's
// deduplicated, classified failure signatures since its last flush. Batch-level Service is
// REQUIRED (an aggregating relay may batch for several services); an empty Issues list is the
// feed's liveness assertion ("alive, nothing failing") and must be attributable.
type IssueBatch struct {
	Service string  `json:"service"`
	Issues  []Issue `json:"issues"`
}

// Issue is one deduplicated failure signature (mesh.md §4.1). Count is a DELTA - occurrences
// since the emitter's previous successful flush, never a cumulative total - so a collector's
// merge (count += delta, firstSeen = min, lastSeen = max, newest exemplars kept) is
// restart-proof and needs no instance identity on the wire.
type Issue struct {
	// Fingerprint is the lowercase hex of the first 16 bytes of SHA-256 over
	// service|topic|version|classification|discriminator (mesh.md §4.1); the collector's merge
	// key. An empty fingerprint is an invalid entry and is skipped, never rejecting the batch.
	Fingerprint    string    `json:"fingerprint"`
	Classification string    `json:"classification"`
	Service        string    `json:"service"`
	Topic          string    `json:"topic"`
	Version        string    `json:"version,omitempty"`
	Transport      string    `json:"transport,omitempty"`
	Status         string    `json:"status"`
	ExceptionType  string    `json:"exceptionType,omitempty"`
	Count          int64     `json:"count"`
	FirstSeen      time.Time `json:"firstSeen"`
	LastSeen       time.Time `json:"lastSeen"`
	// ExemplarTraceIds links the signature to concrete flows; the collector keeps the newest ≤3.
	ExemplarTraceIds []string `json:"exemplarTraceIds,omitempty"`
	ResolutionHint   string   `json:"resolutionHint,omitempty"`
}

// Heartbeat is the body of a mesh:heartbeat message (mesh.md §5.3): the standard
// aggregate health response reused byte-for-byte (no new health vocabulary), wrapped with
// identity and the contract hash - a changed hash is how a collector notices a redeploy
// and knows to re-fetch the descriptor.
type Heartbeat struct {
	Service        string               `json:"service"`
	InstanceID     string               `json:"instanceId,omitempty"`
	DescriptorHash string               `json:"descriptorHash,omitempty"`
	SentAt         time.Time            `json:"sentAt"`
	Health         healthcheck.Response `json:"health"`
}
