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
