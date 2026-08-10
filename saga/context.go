// Package saga is an in-code saga orchestrator for a distributed transaction: an ordered list of
// stages, each a group of steps run concurrently, that either completes in full or rolls back in
// full - leaving no orphaned records, so the whole operation can be safely retried. It is
// zero-dependency and matches Benzene.Saga.
//
// # Model
//
//		Saga  ── ordered ──▶  Stage  ── concurrent ──▶  Step (forward + compensation)
//
//	  - Step (NewStep[T]) - a forward action producing a T result, paired with an optional
//	    compensation that undoes the effect using that result. Success is the result's IsSuccessful.
//	    A forward that panics is recovered and treated as a failed step. Compensation runs during
//	    rollback ONLY if the step succeeded; a succeeded step with no compensation is "nothing to undo".
//	  - Stage (NewStage) - steps run concurrently; the stage succeeds only if every step succeeds. On
//	    failure it compensates its concurrently-succeeded steps.
//	  - Saga (New) - runs stages in order, threading each stage's published results into a shared
//	    SagaContext so a later stage can read an earlier stage's output. On the first stage failure it
//	    compensates every completed effect in reverse (LIFO) order - the failed stage's succeeded steps
//	    first, then each completed stage newest-first - then returns a Result.
//
// # Capability boundary - in-process only, NO durable crash-resume
//
// A saga runs to completion or rollback within a single Run call; steps are in-memory closures that
// cannot be serialized or rehydrated. The optional StateStore records start/stage/finish progress
// for observability and operational recovery (most importantly a PartiallyRolledBack outcome an
// operator must see) - it does NOT let the engine resume a crashed saga. For crash-durable
// long-running orchestration use a durable workflow engine (AWS Step Functions, Azure Durable
// Functions, Temporal). Benzene sagas suit short, in-process compensation flows where a full-process
// failure is acceptable to reconcile manually.
package saga

import (
	"reflect"
	"sync"
)

// SagaContext carries the results of completed saga steps so that a later stage can read what an
// earlier stage produced (e.g. a stage-2 step using the tenant id a stage-1 step created). A
// succeeded step publishes its result here after its stage completes, keyed by the value's type (via
// Set) or by an explicit string key when a stage produces more than one value of the same type.
//
// Steps within a single stage run concurrently but only ever READ earlier stages' values during that
// concurrent phase; writes (Publish) happen single-threaded after each stage's barrier. The RWMutex
// makes that safe even if a step's forward writes a value directly, so misuse cannot data-race.
type SagaContext struct {
	mu    sync.RWMutex
	items map[any]any
}

func newSagaContext() *SagaContext { return &SagaContext{items: map[any]any{}} }

// keyFor derives the context key: an explicit string key when a non-empty one is given, else the
// reflect.Type of T itself. Using the Type value (identity-unique, resolved at set/get time off the
// message dispatch path) rather than its name means two same-named types in different packages never
// collide, and a type key can never be confused with a caller's explicit string key.
func keyFor[T any](key []string) any {
	if len(key) > 0 && key[0] != "" {
		return key[0]
	}
	return reflect.TypeFor[T]()
}

// Set stores value in sc, keyed by an explicit key or, with none, by T's type. Used by a step's
// publish, but callable directly.
func Set[T any](sc *SagaContext, value T, key ...string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.items[keyFor[T](key)] = value
}

// Get returns a previously stored value of type T (by explicit key or by type) and whether it was
// present. Unlike the .NET Get, a missing value is (zero, false), not a panic.
func Get[T any](sc *SagaContext, key ...string) (T, bool) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if stored, ok := sc.items[keyFor[T](key)]; ok {
		if typed, ok := stored.(T); ok {
			return typed, true
		}
	}
	var zero T
	return zero, false
}

// Has reports whether a value of type T (by explicit key or by type) is stored.
func Has[T any](sc *SagaContext, key ...string) bool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	_, ok := sc.items[keyFor[T](key)]
	return ok
}
