package mesh

import (
	"time"

	"github.com/daniellepelley/benzene-go/healthcheck"
)

// The mesh wire-contract topics (mesh.md §4.2): what a service sends to a collector
// (register/heartbeat/traces) and what a view reads back (query:*). The collector side is
// implemented by the meshd package; the names live here because both sides of the wire
// share them. Like every shape in this file, they are proposed for promotion to the main
// repo's spec (mesh.md §8 Phase 5) so other language ports interoperate.
const (
	TopicRegister  = "mesh:register"
	TopicHeartbeat = "mesh:heartbeat"
	TopicTraces    = "mesh:traces"

	TopicQueryFleet   = "mesh:query:fleet"
	TopicQueryService = "mesh:query:service"
	TopicQueryTopic   = "mesh:query:topic"
	TopicQueryTrace   = "mesh:query:trace"
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
