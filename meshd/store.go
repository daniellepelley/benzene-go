package meshd

import (
	"sort"
	"sync"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/mesh"
)

// store is the MVP in-memory state behind a Collector (mesh.md §4.2): cumulative per-
// service and per-topic stats, the latest heartbeat per instance, registered descriptors,
// and a bounded ring of recent trace events (the window consumer edges and the trace
// query are derived from). Everything is derived on the collector side too - a service
// that never registered still appears once its traces do (anonymous but live), and a
// registered service with no traffic appears as a catalog entry with no stats, per the
// design's degradation rule.
type store struct {
	// mu is a plain mutex (not RWMutex): every operation is short, and queries are not
	// hot-path - simplicity wins.
	mu       sync.Mutex
	now      func() time.Time
	capacity int

	services map[string]*serviceState
	topics   map[topicKey]*topicState

	// ring is the bounded trace-event window: append until capacity, then overwrite the
	// oldest (next is the overwrite cursor).
	ring []mesh.TraceEvent
	next int
}

type topicKey struct {
	id      string
	version string
}

type serviceState struct {
	descriptor  *mesh.Descriptor
	instances   map[string]*instanceState
	lastSeen    time.Time
	invocations int64
	errors      int64
	// issues holds this service's deduplicated failure signatures merged by fingerprint.
	issues map[string]*issueState
	// issuesFeedSeen records whether any issue batch (even empty) has arrived - the feed's
	// liveness. Once true, the "issues" missing-feed marker is cleared even with zero issues.
	issuesFeedSeen bool
}

// issueState is one merged failure signature (mesh.md §4.1). count accumulates the wire deltas.
type issueState struct {
	classification   string
	topic            string
	version          string
	transport        string
	status           string
	exceptionType    string
	count            int64
	firstSeen        time.Time
	lastSeen         time.Time
	exemplarTraceIds []string
	resolutionHint   string
}

type instanceState struct {
	healthy        bool
	lastHeartbeat  time.Time
	descriptorHash string
}

type topicState struct {
	providers       map[string]bool
	statusCounts    map[string]int64
	invocations     int64
	errors          int64
	totalDurationMs float64
	lastSeen        time.Time
}

func newStore(capacity int, now func() time.Time) *store {
	return &store{
		now:      now,
		capacity: capacity,
		services: map[string]*serviceState{},
		topics:   map[topicKey]*topicState{},
	}
}

func (s *store) ensureService(name string) *serviceState {
	state, ok := s.services[name]
	if !ok {
		state = &serviceState{instances: map[string]*instanceState{}, issues: map[string]*issueState{}}
		s.services[name] = state
	}
	return state
}

func (s *store) ensureTopic(key topicKey) *topicState {
	state, ok := s.topics[key]
	if !ok {
		state = &topicState{providers: map[string]bool{}, statusCounts: map[string]int64{}}
		s.topics[key] = state
	}
	return state
}

// register stores desc as the service's current contract, replacing any previous
// registration wholesale (including its provider edges - a redeploy that drops a topic
// must drop the provider claim with it).
func (s *store) register(desc mesh.Descriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, topic := range s.topics {
		delete(topic.providers, desc.Service)
	}

	state := s.ensureService(desc.Service)
	state.descriptor = &desc
	state.lastSeen = s.now()

	for _, topic := range desc.Topics {
		s.ensureTopic(topicKey{id: topic.ID, version: topic.Version}).providers[desc.Service] = true
	}
}

// heartbeat records the latest health report for one instance.
func (s *store) heartbeat(hb mesh.Heartbeat) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.ensureService(hb.Service)
	state.lastSeen = s.now()
	state.instances[hb.InstanceID] = &instanceState{
		healthy:        hb.Health.IsHealthy,
		lastHeartbeat:  s.now(),
		descriptorHash: hb.DescriptorHash,
	}
}

// addEvents ingests a trace batch: the ring window plus cumulative per-topic and
// per-service stats (cumulative stats deliberately outlive the ring window). Returns how
// many events were accepted.
func (s *store) addEvents(events []mesh.TraceEvent) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, event := range events {
		if len(s.ring) < s.capacity {
			s.ring = append(s.ring, event)
		} else {
			s.ring[s.next] = event
			s.next = (s.next + 1) % s.capacity
		}

		failed := benzene.Status(event.Status).IsFailure()

		topic := s.ensureTopic(topicKey{id: event.Topic, version: event.TopicVersion})
		topic.invocations++
		topic.statusCounts[event.Status]++
		topic.totalDurationMs += event.DurationMs
		topic.lastSeen = s.now()
		if failed {
			topic.errors++
		}

		if event.Service != "" {
			service := s.ensureService(event.Service)
			service.invocations++
			service.lastSeen = s.now()
			if failed {
				service.errors++
			}
		}
	}
	return len(events)
}

// addIssues merges an issue batch into the store by fingerprint (mesh.md §4.1). Receiving any
// batch - even an empty one - marks the service's issue feed as live. Count is a delta, so a
// repeat fingerprint accumulates (count += delta, firstSeen = min, lastSeen = max, newest ≤3
// exemplars kept, other fields latest-wins). An entry with an empty fingerprint is invalid and
// skipped, never rejecting the batch; the return is the number of valid entries accepted.
func (s *store) addIssues(batch mesh.IssueBatch) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	service := s.ensureService(batch.Service)
	service.lastSeen = s.now()
	service.issuesFeedSeen = true

	accepted := 0
	for _, issue := range batch.Issues {
		if issue.Fingerprint == "" {
			continue // invalid entry - skipped, never rejected
		}
		accepted++

		merged := service.issues[issue.Fingerprint]
		if merged == nil {
			merged = &issueState{}
			service.issues[issue.Fingerprint] = merged
		}
		merged.count += issue.Count
		merged.classification = issue.Classification
		merged.topic = issue.Topic
		merged.version = issue.Version
		merged.transport = issue.Transport
		merged.status = issue.Status
		merged.exceptionType = issue.ExceptionType
		merged.resolutionHint = issue.ResolutionHint
		if !issue.FirstSeen.IsZero() && (merged.firstSeen.IsZero() || issue.FirstSeen.Before(merged.firstSeen)) {
			merged.firstSeen = issue.FirstSeen
		}
		if issue.LastSeen.After(merged.lastSeen) {
			merged.lastSeen = issue.LastSeen
		}
		merged.exemplarTraceIds = newestExemplars(merged.exemplarTraceIds, issue.ExemplarTraceIds)
	}
	return accepted
}

// maxExemplars bounds the exemplar trace ids kept per merged issue (mesh.md §4.1: newest ≤3).
const maxExemplars = 3

// newestExemplars appends the delta's exemplars after the existing ones (the delta is newer) and
// keeps the newest maxExemplars.
func newestExemplars(existing, delta []string) []string {
	combined := append(existing, delta...)
	if len(combined) > maxExemplars {
		combined = combined[len(combined)-maxExemplars:]
	}
	return combined
}

// consumersByTopic derives who-calls-whom from the ring window: an event's parent span
// belonging to another service makes that service a consumer of the event's topic. Caller
// holds s.mu. Unmeshed callers have no parent span in the window and appear as no edge,
// per the design ("anonymous edges" resolve to nothing rather than a guess).
func (s *store) consumersByTopic() map[topicKey]map[string]bool {
	spanService := make(map[string]string, len(s.ring))
	for _, event := range s.ring {
		if event.Service != "" {
			spanService[event.SpanID] = event.Service
		}
	}

	consumers := map[topicKey]map[string]bool{}
	for _, event := range s.ring {
		if event.ParentSpanID == "" {
			continue
		}
		caller, ok := spanService[event.ParentSpanID]
		if !ok || caller == event.Service {
			continue
		}
		key := topicKey{id: event.Topic, version: event.TopicVersion}
		if consumers[key] == nil {
			consumers[key] = map[string]bool{}
		}
		consumers[key][caller] = true
	}
	return consumers
}

func (s *store) fleet() FleetView {
	s.mu.Lock()
	defer s.mu.Unlock()

	view := FleetView{
		GeneratedAt: s.now(),
		Services:    []ServiceSummary{},
		Topics:      []TopicSummary{},
		Traces:      []TraceSummary{},
		Issues:      []IssueView{},
	}
	for name := range s.services {
		view.Services = append(view.Services, s.serviceSummary(name))
	}
	sort.Slice(view.Services, func(i, j int) bool { return view.Services[i].Service < view.Services[j].Service })

	view.Issues = s.fleetIssues()

	consumers := s.consumersByTopic()
	for key := range s.topics {
		view.Topics = append(view.Topics, s.topicSummary(key, consumers[key]))
	}
	sort.Slice(view.Topics, func(i, j int) bool {
		if view.Topics[i].Topic != view.Topics[j].Topic {
			return view.Topics[i].Topic < view.Topics[j].Topic
		}
		return view.Topics[i].Version < view.Topics[j].Version
	})

	view.Traces = s.traceSummaries(maxFleetTraces)
	return view
}

// fleetIssues flattens every service's merged issues into the fleet view, sorted by service then
// fingerprint for a stable rendering. Caller holds s.mu.
func (s *store) fleetIssues() []IssueView {
	issues := []IssueView{}
	for name, state := range s.services {
		for fingerprint, issue := range state.issues {
			issues = append(issues, IssueView{
				Fingerprint:      fingerprint,
				Classification:   issue.classification,
				Service:          name,
				Topic:            issue.topic,
				Version:          issue.version,
				Transport:        issue.transport,
				Status:           issue.status,
				ExceptionType:    issue.exceptionType,
				Count:            issue.count,
				FirstSeen:        issue.firstSeen,
				LastSeen:         issue.lastSeen,
				ExemplarTraceIds: issue.exemplarTraceIds,
				ResolutionHint:   issue.resolutionHint,
			})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Service != issues[j].Service {
			return issues[i].Service < issues[j].Service
		}
		return issues[i].Fingerprint < issues[j].Fingerprint
	})
	return issues
}

// maxFleetTraces bounds the recent-flows list on the fleet view.
const maxFleetTraces = 20

// serviceSummary builds one service's row. Caller holds s.mu.
func (s *store) serviceSummary(name string) ServiceSummary {
	state := s.services[name]
	summary := ServiceSummary{
		Service:     name,
		Health:      healthUnknown,
		LastSeen:    state.lastSeen,
		Instances:   len(state.instances),
		Invocations: state.invocations,
		Errors:      state.errors,
	}

	if state.descriptor != nil {
		summary.Runtime = state.descriptor.Runtime
		summary.Binding = state.descriptor.Binding
		summary.Placement = state.descriptor.Placement
		summary.Topics = len(state.descriptor.Topics)
	} else {
		// Known only from traffic: anonymous but live, and visibly reduced.
		summary.MissingFeeds = append(summary.MissingFeeds, "descriptor")
	}
	if len(state.instances) == 0 {
		summary.MissingFeeds = append(summary.MissingFeeds, "health")
	} else {
		summary.Health = healthHealthy
		for _, instance := range state.instances {
			if !instance.healthy {
				summary.Health = healthDegraded
			}
		}
	}
	if state.invocations == 0 {
		summary.MissingFeeds = append(summary.MissingFeeds, "traces")
	}
	// The issues feed is only flagged missing when a failure needs explaining: the service has
	// recorded failures but has never sent an issue batch to explain them (mesh.md §4.1). A
	// service with no failures, or one whose issue feed is live (even with zero issues), is not
	// reduced by its absence.
	if state.errors > 0 && !state.issuesFeedSeen {
		summary.MissingFeeds = append(summary.MissingFeeds, "issues")
	}
	return summary
}

// topicSummary builds one topic's row. Caller holds s.mu.
func (s *store) topicSummary(key topicKey, consumers map[string]bool) TopicSummary {
	state := s.topics[key]
	summary := TopicSummary{
		Topic:        key.id,
		Version:      key.version,
		Providers:    sortedKeys(state.providers),
		Consumers:    sortedKeys(consumers),
		Invocations:  state.invocations,
		Errors:       state.errors,
		StatusCounts: map[string]int64{},
		LastSeen:     state.lastSeen,
	}
	for status, count := range state.statusCounts {
		summary.StatusCounts[status] = count
	}
	if state.invocations > 0 {
		summary.AvgDurationMs = state.totalDurationMs / float64(state.invocations)
	}
	return summary
}

// traceSummaries groups the ring window by trace id, newest first. Caller holds s.mu.
func (s *store) traceSummaries(limit int) []TraceSummary {
	byTrace := map[string][]mesh.TraceEvent{}
	for _, event := range s.ring {
		byTrace[event.TraceID] = append(byTrace[event.TraceID], event)
	}

	summaries := make([]TraceSummary, 0, len(byTrace))
	for traceID, events := range byTrace {
		summary := TraceSummary{TraceID: traceID, Events: len(events), StartedAt: events[0].StartedAt}
		var end time.Time
		services := map[string]bool{}
		for _, event := range events {
			if event.StartedAt.Before(summary.StartedAt) {
				summary.StartedAt = event.StartedAt
			}
			if finished := event.StartedAt.Add(time.Duration(event.DurationMs * float64(time.Millisecond))); finished.After(end) {
				end = finished
			}
			if event.Service != "" {
				services[event.Service] = true
			}
			if benzene.Status(event.Status).IsFailure() {
				summary.Failed = true
			}
		}
		summary.DurationMs = float64(end.Sub(summary.StartedAt)) / float64(time.Millisecond)
		summary.Services = sortedKeys(services)
		summaries = append(summaries, summary)
	}

	sort.Slice(summaries, func(i, j int) bool { return summaries[i].StartedAt.After(summaries[j].StartedAt) })
	if len(summaries) > limit {
		summaries = summaries[:limit]
	}
	return summaries
}

func (s *store) service(name string) (ServiceView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.services[name]
	if !ok {
		return ServiceView{}, false
	}

	view := ServiceView{ServiceSummary: s.serviceSummary(name), Descriptor: state.descriptor}
	for id, instance := range state.instances {
		instanceView := InstanceView{
			InstanceID:     id,
			Healthy:        instance.healthy,
			LastHeartbeat:  instance.lastHeartbeat,
			DescriptorHash: instance.descriptorHash,
		}
		if state.descriptor != nil && state.descriptor.DescriptorHash != "" && instance.descriptorHash != "" {
			matches := instance.descriptorHash == state.descriptor.DescriptorHash
			instanceView.HashMatches = &matches
		}
		view.Instances = append(view.Instances, instanceView)
	}
	sort.Slice(view.Instances, func(i, j int) bool { return view.Instances[i].InstanceID < view.Instances[j].InstanceID })
	return view, true
}

func (s *store) topic(id, version string) (TopicSummary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := topicKey{id: id, version: version}
	if _, ok := s.topics[key]; !ok {
		return TopicSummary{}, false
	}
	return s.topicSummary(key, s.consumersByTopic()[key]), true
}

func (s *store) trace(traceID string) (TraceView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	view := TraceView{TraceID: traceID, Events: []mesh.TraceEvent{}}
	for _, event := range s.ring {
		if event.TraceID == traceID {
			view.Events = append(view.Events, event)
		}
	}
	if len(view.Events) == 0 {
		return TraceView{}, false
	}
	sort.Slice(view.Events, func(i, j int) bool { return view.Events[i].StartedAt.Before(view.Events[j].StartedAt) })
	return view, true
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
