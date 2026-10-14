package mesh

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/daniellepelley/benzene-go/healthcheck"
)

// The mesh wire-contract topics (mesh.md §4): what a service sends to a collector
// (register/heartbeat/traces/issues) and what a view reads back (query:*). The register body is the
// service's contract.Document (mesh.md §2). The collector side is
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
	// Errors is the structured detail behind the failures counted (mesh.md §4.1): field and code
	// pairs from the failing results' problem documents, never the message, each with a delta count,
	// bounded to MaxIssueErrors. Not part of the fingerprint.
	Errors IssueErrors `json:"errors,omitempty"`
}

// IssueError is one entry of an issue's error detail (mesh.md §4.1). Its identity is the (Field,
// Code) pair; an empty string is an absent member. Count is a delta on the wire, like Issue.Count.
type IssueError struct {
	Field string `json:"field,omitempty"`
	Code  string `json:"code,omitempty"`
	Count int64  `json:"count"`
}

// IssueErrors decodes leniently: an entry that is not an object, has neither field nor code, or
// whose count is not a positive integer is skipped, so one malformed entry never costs the issue
// (or the batch) it arrived in (mesh.md §4.1).
type IssueErrors []IssueError

// UnmarshalJSON implements the lenient decoding described on IssueErrors.
func (e *IssueErrors) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		*e = nil
		return nil
	}
	entries := IssueErrors{}
	for _, item := range raw {
		var entry struct {
			Field *string      `json:"field"`
			Code  *string      `json:"code"`
			Count *json.Number `json:"count"`
		}
		decoder := json.NewDecoder(bytes.NewReader(item))
		decoder.UseNumber()
		if decoder.Decode(&entry) != nil || entry.Count == nil {
			continue
		}
		count, err := entry.Count.Int64()
		if err != nil || count <= 0 {
			continue
		}
		var field, code string
		if entry.Field != nil {
			field = *entry.Field
		}
		if entry.Code != nil {
			code = *entry.Code
		}
		if field == "" && code == "" {
			continue
		}
		entries = append(entries, IssueError{Field: field, Code: code, Count: count})
	}
	*e = entries
	return nil
}

// Heartbeat is the body of a benzene:mesh:heartbeat message (mesh.md §5): the standard aggregate
// health response reused byte-for-byte (no new health vocabulary), wrapped with identity and the
// registered document's registrationHash - a hash that differs from the registration's is how a
// collector notices an instance running a contract it hasn't learned. ServiceVersion is the
// instance's release, omitted when it declares none; it scopes the comparison in a collector that
// implements Service Versions. NewHeartbeat builds one from the registered document.
type Heartbeat struct {
	Service          string               `json:"service"`
	ServiceVersion   string               `json:"serviceVersion,omitempty"`
	InstanceID       string               `json:"instanceId,omitempty"`
	RegistrationHash string               `json:"registrationHash,omitempty"`
	SentAt           time.Time            `json:"sentAt"`
	Health           healthcheck.Response `json:"health"`
}
