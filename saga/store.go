package saga

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// StateStore is a pluggable sink for a saga's progress and outcome, so a rolled-back or (most
// importantly) partially-rolled-back run's last known state is durably recorded for an operator to
// see. It records progress; it does NOT resume a saga - the engine's steps are in-process closures
// that cannot be rehydrated (see the package doc). The engine calls the methods in order -
// RecordStarted, then RecordStageCompleted per completed stage, then RecordFinished - once per
// attempt, threading the run's ctx through. Recording is best-effort observability: a store method
// returns nothing, so a store that surfaces its own errors by logging/returning never fails the saga.
// The engine does not recover a store call, so an adapter MUST NOT panic - handle a backend outage
// internally (log and move on), never with a panic that would escape Run/RunWith to the caller.
type StateStore interface {
	// RecordStarted records that a saga run attempt has started.
	RecordStarted(ctx context.Context, run RunInfo)
	// RecordStageCompleted records that a stage completed successfully within an attempt.
	RecordStageCompleted(ctx context.Context, sagaID string, attempt, stageIndex int)
	// RecordFinished records the final outcome of an attempt.
	RecordFinished(ctx context.Context, sagaID string, attempt int, result Result)
}

// StateEventKind is the kind of progress event recorded to a StateStore.
type StateEventKind string

const (
	// EventStarted - a saga run attempt started.
	EventStarted StateEventKind = "started"
	// EventStageCompleted - a stage completed successfully within an attempt.
	EventStageCompleted StateEventKind = "stage-completed"
	// EventFinished - an attempt finished (with its Result).
	EventFinished StateEventKind = "finished"
)

// StateEvent is one recorded saga-progress event, as accumulated by InMemoryStateStore. A durable
// store adapter typically persists the same fields to a row/document instead.
type StateEvent struct {
	SagaID  string
	Attempt int
	Kind    StateEventKind
	// StageIndex is the completed stage index, for an EventStageCompleted event (else -1).
	StageIndex int
	// Result is the attempt's result, for an EventFinished event (else the zero Result).
	Result Result
}

// InMemoryStateStore is an in-process StateStore that accumulates progress events in a slice - for
// tests, local development, and inspecting what a saga did. A production deployment supplies a
// durable store (a table/document write) so a rolled-back or partially-rolled-back outcome survives a
// restart. Thread-safe: a saga runs stages sequentially, but the store is safe to share across sagas.
type InMemoryStateStore struct {
	mu     sync.Mutex
	events []StateEvent
}

// NewInMemoryStateStore builds an empty in-memory store.
func NewInMemoryStateStore() *InMemoryStateStore { return &InMemoryStateStore{} }

// Events returns a snapshot of every recorded event, in order.
func (s *InMemoryStateStore) Events() []StateEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StateEvent(nil), s.events...)
}

// EventsFor returns the recorded events for one saga instance, in order.
func (s *InMemoryStateStore) EventsFor(sagaID string) []StateEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StateEvent
	for _, e := range s.events {
		if e.SagaID == sagaID {
			out = append(out, e)
		}
	}
	return out
}

// RecordStarted implements StateStore.
func (s *InMemoryStateStore) RecordStarted(_ context.Context, run RunInfo) {
	s.add(StateEvent{SagaID: run.SagaID, Attempt: run.Attempt, Kind: EventStarted, StageIndex: -1})
}

// RecordStageCompleted implements StateStore.
func (s *InMemoryStateStore) RecordStageCompleted(_ context.Context, sagaID string, attempt, stageIndex int) {
	s.add(StateEvent{SagaID: sagaID, Attempt: attempt, Kind: EventStageCompleted, StageIndex: stageIndex})
}

// RecordFinished implements StateStore.
func (s *InMemoryStateStore) RecordFinished(_ context.Context, sagaID string, attempt int, result Result) {
	s.add(StateEvent{SagaID: sagaID, Attempt: attempt, Kind: EventFinished, StageIndex: -1, Result: result})
}

func (s *InMemoryStateStore) add(e StateEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

// newSagaID returns a random 128-bit hex id for correlating a run's store events. crypto/rand.Read
// effectively never fails; on the astronomically-rare short read the (possibly-zero) bytes are still
// a usable correlation id, so the error is deliberately ignored to keep the id allocation total.
func newSagaID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
