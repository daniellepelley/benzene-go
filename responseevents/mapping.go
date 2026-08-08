// Package responseevents republishes a handler's response payload as a follow-up event on a
// fire-and-forget transport - the *response-as-event* pattern, matching Benzene.ResponseEvents.
//
// A request/response handler on a queue transport (e.g. an SQS `order:create`) returns a payload
// the transport can't deliver back to a caller; a per-pipeline mapping publishes that payload as an
// event (`order:created`) through the outbound Sender instead. It is a pipeline Middleware that runs
// after the handler: every Mapping that matches the (source topic, result) pair publishes (fan-out
// is allowed), and a publish failure follows the configured PublishFailureMode.
//
// Scope: this is the runtime capability (mappings + middleware + the Publisher port). The .NET
// package's AsyncAPI/event-service spec-catalog half (IResponseEventCatalog, declaration-only
// definitions, the unmapped-response build-time diagnostic) is deliberately not ported - Go has no
// AsyncAPI generator here; the mesh descriptor derivation (mesh/) is this port's introspection path.
// Mapping.Covers is kept so a future diagnostic can be built on the same static coverage rule.
//
// Nil-payload semantics: a mapping publishes only when the result carries a payload. "Carries a
// payload" is a plain (reflect-free) nil check, since this is the per-message dispatch path and this
// port reserves reflection for startup only. So a genuinely absent payload (e.g. Ignored[any](nil))
// is skipped, but a successful result whose payload is a *typed* nil pointer (e.g. Ok[*T](nil)) is
// treated as present and published as JSON null - detecting a typed nil would need reflection here.
// Return a non-success status, or a Project that returns nil, to suppress such a publish explicitly.
package responseevents

import (
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
)

// Publication is the outcome of a matched Mapping: the event topic to publish on and the payload to
// publish (never nil - a mapping with no payload resolves to no publication instead).
type Publication struct {
	EventTopic benzene.Topic
	Payload    any
}

// Mapping is one rule for turning a handler's response into a follow-up event. Register mappings on
// the Middleware; each is evaluated after the handler runs, and every mapping that resolves a
// Publication publishes. Custom rules (multi-topic, payload-dependent topics) implement this
// interface directly; Map and CrudConvention are the ready-made ones.
type Mapping interface {
	// Resolve decides whether this mapping fires for the handled message, and with what. It returns
	// the event to publish, or nil if the mapping does not apply.
	Resolve(sourceTopic benzene.Topic, result benzene.ResultInfo) *Publication
	// Covers reports whether this mapping could publish an event for a successful response on the
	// given source topic - a static coverage check (for a future unmapped-response diagnostic),
	// independent of any runtime result, status, or payload.
	Covers(sourceTopic benzene.Topic) bool
	// Description is a human-readable summary of the rule, for diagnostics and introspection.
	Description() string
}

// MapOption configures an explicit Map mapping.
type MapOption func(*explicitMapping)

// When replaces the default "publish on a successful result" predicate with a custom one. The
// non-nil-payload requirement always still applies (there is no event to publish without a payload).
func When(predicate func(benzene.ResultInfo) bool) MapOption {
	return func(m *explicitMapping) { m.when = predicate }
}

// Project reshapes the response payload into the event payload before publishing. Returning nil
// from the projection skips the publish for that message.
func Project(project func(payload any) any) MapOption {
	return func(m *explicitMapping) { m.project = project }
}

// Map maps one source topic's successful, payload-carrying responses to an event topic. By default
// it fires when the handler's result is successful and carries a payload; When overrides the status
// check and Project reshapes the payload.
func Map(sourceTopic, eventTopic string, opts ...MapOption) Mapping {
	m := &explicitMapping{source: sourceTopic, event: eventTopic}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

type explicitMapping struct {
	source, event string
	when          func(benzene.ResultInfo) bool // nil - default to result success
	project       func(any) any                 // nil - identity; returns nil to skip
}

func (m *explicitMapping) Resolve(sourceTopic benzene.Topic, result benzene.ResultInfo) *Publication {
	if !strings.EqualFold(sourceTopic.ID, m.source) {
		return nil
	}

	fires := resultSuccessful(result)
	if m.when != nil {
		fires = m.when(result)
	}
	if !fires {
		return nil
	}

	payload := result.ResultPayload()
	if payload == nil {
		return nil
	}
	if m.project != nil {
		payload = m.project(payload)
		if payload == nil {
			return nil
		}
	}
	return &Publication{EventTopic: benzene.NewTopic(m.event), Payload: payload}
}

func (m *explicitMapping) Covers(sourceTopic benzene.Topic) bool {
	return strings.EqualFold(sourceTopic.ID, m.source)
}

func (m *explicitMapping) Description() string {
	desc := m.source + " -> " + m.event
	if m.when != nil {
		desc += " [conditional]"
	}
	return desc
}

// crudVerbToStatus maps a topic's CRUD verb to the result status that must accompany it for the
// convention mapping to fire.
var crudVerbToStatus = map[string]benzene.Status{
	"create": benzene.StatusCreated,
	"update": benzene.StatusUpdated,
	"delete": benzene.StatusDeleted,
}

// CrudConvention returns the CRUD naming-convention mapping: a topic whose last `:`-segment is
// create/update/delete, handled with the matching status (created/updated/deleted) and a payload,
// publishes that payload on the past-tense topic (`order:create` -> `order:created`).
func CrudConvention() Mapping { return crudMapping{} }

type crudMapping struct{}

func (crudMapping) Resolve(sourceTopic benzene.Topic, result benzene.ResultInfo) *Publication {
	requiredStatus, ok := crudVerbToStatus[crudVerb(sourceTopic)]
	if !ok || result.ResultStatus() != requiredStatus {
		return nil
	}
	payload := result.ResultPayload()
	if payload == nil {
		return nil
	}
	// create->created, update->updated, delete->deleted: the full topic id + "d".
	return &Publication{EventTopic: benzene.NewTopic(sourceTopic.ID + "d"), Payload: payload}
}

func (crudMapping) Covers(sourceTopic benzene.Topic) bool {
	_, ok := crudVerbToStatus[crudVerb(sourceTopic)]
	return ok
}

func (crudMapping) Description() string {
	return "CRUD convention: <entity>:create|update|delete -> <entity>:created|updated|deleted on created/updated/deleted"
}

// crudVerb returns the last `:`-segment of a topic id (the CRUD verb), or the whole id if unsegmented.
func crudVerb(topic benzene.Topic) string {
	id := topic.ID
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}
