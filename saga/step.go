package saga

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
)

// StepState is the lifecycle state of a single saga Step within a run.
type StepState string

const (
	// StepPending - the step has not run yet.
	StepPending StepState = "pending"
	// StepSucceeded - the forward action ran and returned a successful result.
	StepSucceeded StepState = "succeeded"
	// StepFailed - the forward action ran and returned a failed result, or panicked.
	StepFailed StepState = "failed"
	// StepRolledBack - the step had succeeded and its compensation ran successfully during rollback.
	StepRolledBack StepState = "rolled-back"
	// StepCompensationFailed - the step had succeeded but its compensation itself failed (or
	// panicked); the effect it created may still exist and needs attention.
	StepCompensationFailed StepState = "compensation-failed"
)

// Step is the type-erased view of a saga step the engine operates on - the forward/publish/
// compensate machinery is unexported so only this package drives a step, while State/Result/Err let a
// caller inspect a step reported in a Result's CompensationFailures. Build one with NewStep; the
// concrete type carries the forward action's result type, recovered here via benzene.ResultInfo (the
// same type-erasure the Registry uses for handlers).
type Step interface {
	// State reports the step's current lifecycle state.
	State() StepState
	// Result is the forward action's result once it has run, or nil before it runs.
	Result() benzene.ResultInfo
	// Err is the value a forward or compensation panicked with (wrapped), or nil.
	Err() error

	execute(ctx context.Context, sc *SagaContext)
	publish(sc *SagaContext)
	compensate(ctx context.Context, sc *SagaContext) bool
}

// Forward is a step's forward action: it reads any earlier-stage values it needs from the context
// and returns a T result. A non-successful result (or a panic) fails the step and its stage.
type Forward[T any] func(ctx context.Context, sc *SagaContext) benzene.Result[T]

// Compensate undoes a succeeded step's effect, given the context and the forward action's payload. A
// non-successful result (or a panic) marks the step CompensationFailed - an orphaned effect surfaced
// on the Result. Pass a nil Compensate for a step that creates no effect to undo.
type Compensate[T any] func(ctx context.Context, sc *SagaContext, payload T) benzene.Result[struct{}]

// NewStep builds a saga step whose forward action produces a T result, with an optional compensation
// (nil for a step with no effect to undo). The successful result is published to the SagaContext
// keyed by T; use NewKeyedStep when a stage produces more than one value of the same type.
func NewStep[T any](forward Forward[T], compensate Compensate[T]) Step {
	return &step[T]{forward: forward, compensateFn: compensate}
}

// NewKeyedStep is NewStep with an explicit SagaContext key for the published result.
func NewKeyedStep[T any](key string, forward Forward[T], compensate Compensate[T]) Step {
	return &step[T]{forward: forward, compensateFn: compensate, key: key}
}

type step[T any] struct {
	forward      Forward[T]
	compensateFn Compensate[T]
	key          string

	state     StepState
	result    benzene.Result[T]
	hasResult bool
	err       error
}

func (s *step[T]) State() StepState { return orPending(s.state) }

func (s *step[T]) Result() benzene.ResultInfo {
	if !s.hasResult {
		return nil
	}
	return s.result
}

func (s *step[T]) Err() error { return s.err }

// execute runs the forward action, recovering a panic into a failed result (matching the .NET
// engine's catch: a forward that throws is a failed step, not a crash). Err is reset first because a
// step instance is reused across retry attempts.
func (s *step[T]) execute(ctx context.Context, sc *SagaContext) {
	s.err = nil
	defer func() {
		if r := recover(); r != nil {
			s.err = fmt.Errorf("saga step panicked: %v", r)
			s.result = benzene.UnexpectedError[T](s.err.Error())
			s.hasResult = true
			s.state = StepFailed
		}
	}()

	s.result = s.forward(ctx, sc)
	s.hasResult = true
	if s.result.IsSuccessful() {
		s.state = StepSucceeded
	} else {
		s.state = StepFailed
	}
}

// publish writes the step's successful payload into the context so later stages can read it. Called
// only after the step's stage has fully succeeded.
func (s *step[T]) publish(sc *SagaContext) {
	if s.state == StepSucceeded && s.hasResult {
		Set(sc, s.payload(), s.key)
	}
}

// payload dereferences the forward result's payload pointer (nil-safe: a successful benzene.Result
// always carries a payload, but a directly-constructed one might not).
func (s *step[T]) payload() T {
	if s.result.Payload != nil {
		return *s.result.Payload
	}
	var zero T
	return zero
}

// compensate undoes the step during rollback: a no-op success if the step did not succeed or has no
// compensation, else it runs the compensation (recovering a panic as a compensation failure).
func (s *step[T]) compensate(ctx context.Context, sc *SagaContext) bool {
	if s.state != StepSucceeded {
		return true
	}
	if s.compensateFn == nil {
		s.state = StepRolledBack
		return true
	}

	ok := func() (ok bool) {
		defer func() {
			if r := recover(); r != nil {
				s.err = fmt.Errorf("saga compensation panicked: %v", r)
				ok = false
			}
		}()
		return s.compensateFn(ctx, sc, s.payload()).IsSuccessful()
	}()

	if ok {
		s.state = StepRolledBack
		return true
	}
	s.state = StepCompensationFailed
	return false
}

// orPending maps the zero-value state to StepPending so a never-run step reports cleanly.
func orPending(state StepState) StepState {
	if state == "" {
		return StepPending
	}
	return state
}
