package saga

import (
	"context"
	"sync"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// Outcome is the overall outcome of running a Saga.
type Outcome string

const (
	// OutcomeSucceeded - every stage completed successfully.
	OutcomeSucceeded Outcome = "succeeded"
	// OutcomeRolledBack - a stage failed and every effect created so far was successfully
	// compensated; the system is back at its starting state and the saga can be retried.
	OutcomeRolledBack Outcome = "rolled-back"
	// OutcomePartiallyRolledBack - a stage failed, rollback ran, but at least one compensation itself
	// failed; the system may be left with orphaned effects that need manual attention (see
	// Result.CompensationFailures).
	OutcomePartiallyRolledBack Outcome = "partially-rolled-back"
)

// Result is the outcome of running a Saga: whether it succeeded, and if not, which stage failed,
// why, and whether rollback was clean.
type Result struct {
	// Outcome is the overall outcome.
	Outcome Outcome
	// FailedStageIndex is the zero-based index of the stage that failed, or -1 on success.
	FailedStageIndex int
	// Failure is the failing step's result, or nil on success.
	Failure benzene.ResultInfo
	// FailureErr is the value the failing step's forward panicked with (wrapped), or nil.
	FailureErr error
	// CompensationFailures are the steps whose compensation itself failed during rollback - non-empty
	// only when Outcome is OutcomePartiallyRolledBack. Their effects may still exist.
	CompensationFailures []Step
}

// IsSuccess reports whether the saga completed successfully.
func (r Result) IsSuccess() bool { return r.Outcome == OutcomeSucceeded }

// Stage is a group of steps run concurrently as one all-or-nothing unit within a Saga. The stage
// succeeds only if every step succeeds.
type Stage struct {
	steps []Step
}

// NewStage groups steps into a concurrently-run stage.
func NewStage(steps ...Step) Stage { return Stage{steps: steps} }

// execute runs every step's forward concurrently (awaiting all, even if one fails early) and reports
// whether the whole stage succeeded.
func (s Stage) execute(ctx context.Context, sc *SagaContext) bool {
	var wg sync.WaitGroup
	for _, st := range s.steps {
		wg.Add(1)
		go func(st Step) {
			defer wg.Done()
			st.execute(ctx, sc)
		}(st)
	}
	wg.Wait()

	for _, st := range s.steps {
		if st.State() != StepSucceeded {
			return false
		}
	}
	return true
}

// publish writes every succeeded step's result into the context. Called only after the stage fully
// succeeds.
func (s Stage) publish(sc *SagaContext) {
	for _, st := range s.steps {
		st.publish(sc)
	}
}

// compensate undoes this stage's succeeded steps concurrently (best effort - every compensation is
// attempted regardless of whether an earlier one failed) and reports whether all were clean.
func (s Stage) compensate(ctx context.Context, sc *SagaContext) bool {
	oks := make([]bool, len(s.steps))
	var wg sync.WaitGroup
	for i, st := range s.steps {
		wg.Add(1)
		go func(i int, st Step) {
			defer wg.Done()
			oks[i] = st.compensate(ctx, sc)
		}(i, st)
	}
	wg.Wait()

	for _, ok := range oks {
		if !ok {
			return false
		}
	}
	return true
}

// Saga is an in-code orchestrator for a distributed transaction: an ordered list of stages, each a
// group of steps run concurrently. It is all-or-nothing - it either completes in full or rolls back
// in full. See the package doc for the model and the in-process capability boundary.
type Saga struct {
	stages []Stage
}

// New builds a Saga from stages, run in the given order.
func New(stages ...Stage) *Saga { return &Saga{stages: stages} }

// Run executes the saga once with no store and no retry - the default, zero-overhead behavior. On
// the first stage failure it compensates every completed effect in reverse (LIFO) order and returns
// a rolled-back Result.
func (s *Saga) Run(ctx context.Context) Result {
	return s.runOnce(ctx, RunOptions{Name: "saga"}, 1, "")
}

// RunWith executes the saga with the given options - an optional durable StateStore and an optional
// RetryPolicy. With a retry policy, a CLEAN rollback (OutcomeRolledBack) is re-run from scratch up to
// the policy's attempt limit; a success needs none, and a partially-rolled-back outcome (which may
// have left effects a retry would double-apply) is never retried.
func (s *Saga) RunWith(ctx context.Context, opts RunOptions) Result {
	if opts.Name == "" {
		opts.Name = "saga"
	}

	maxAttempts := 1
	delay := time.Duration(0)
	if opts.RetryPolicy != nil {
		maxAttempts = opts.RetryPolicy.maxAttempts()
		delay = opts.RetryPolicy.InitialDelay
	}

	// A single, stable id shared across every attempt of this run, generated up front only when a
	// store is present (so the plain no-store path allocates nothing).
	sagaID := opts.SagaID
	if sagaID == "" && opts.StateStore != nil {
		sagaID = newSagaID()
	}

	// maxAttempts() is always >= 1, so attempt == maxAttempts is reached and the loop always returns
	// from inside - an unconditional for (a terminating statement) avoids a dead trailing return.
	for attempt := 1; ; attempt++ {
		result := s.runOnce(ctx, opts, attempt, sagaID)

		// Only a clean rollback is safe to retry; stop otherwise or when attempts are exhausted.
		if result.Outcome != OutcomeRolledBack || attempt == maxAttempts {
			return result
		}
		if delay > 0 {
			opts.RetryPolicy.sleep(ctx, delay)
			delay = time.Duration(float64(delay) * opts.RetryPolicy.backoffFactor())
		}
	}
}

func (s *Saga) runOnce(ctx context.Context, opts RunOptions, attempt int, sagaID string) Result {
	store := opts.StateStore
	if store != nil {
		store.RecordStarted(ctx, RunInfo{SagaID: sagaID, Name: opts.Name, Attempt: attempt, StageCount: len(s.stages)})
	}

	sc := newSagaContext()
	var completed []Stage

	for i, stage := range s.stages {
		if stage.execute(ctx, sc) {
			stage.publish(sc)
			completed = append(completed, stage)
			if store != nil {
				store.RecordStageCompleted(ctx, sagaID, attempt, i)
			}
			continue
		}

		// Stage i failed. Roll back this stage's concurrently-succeeded steps first, then every
		// completed stage newest-first, so effects are undone in the reverse of the order created.
		clean := rollBack(ctx, sc, completed, stage)
		outcome := OutcomeRolledBack
		if !clean {
			outcome = OutcomePartiallyRolledBack
		}

		result := Result{
			Outcome:              outcome,
			FailedStageIndex:     i,
			CompensationFailures: collectCompensationFailures(completed, stage),
		}
		// The failing step's result/error, for reporting: the first step in the stage whose forward
		// failed (rather than a concurrently-succeeded sibling). A failed stage always has one.
		for _, st := range stage.steps {
			if st.State() == StepFailed {
				result.Failure = st.Result()
				result.FailureErr = st.Err()
				break
			}
		}
		if store != nil {
			store.RecordFinished(ctx, sagaID, attempt, result)
		}
		return result
	}

	result := Result{Outcome: OutcomeSucceeded, FailedStageIndex: -1}
	if store != nil {
		store.RecordFinished(ctx, sagaID, attempt, result)
	}
	return result
}

// rollBack compensates the failed stage's succeeded steps first, then every completed stage
// newest-first (LIFO), and reports whether every compensation was clean.
func rollBack(ctx context.Context, sc *SagaContext, completed []Stage, failed Stage) bool {
	clean := failed.compensate(ctx, sc)
	for j := len(completed) - 1; j >= 0; j-- {
		stageClean := completed[j].compensate(ctx, sc)
		clean = clean && stageClean
	}
	return clean
}

// collectCompensationFailures gathers every step (across the completed stages and the failed one)
// whose compensation itself failed - the orphaned effects surfaced on the Result.
func collectCompensationFailures(completed []Stage, failed Stage) []Step {
	var failures []Step
	collect := func(stage Stage) {
		for _, st := range stage.steps {
			if st.State() == StepCompensationFailed {
				failures = append(failures, st)
			}
		}
	}
	for _, stage := range completed {
		collect(stage)
	}
	collect(failed) // kept separate: appending it onto `completed` would alias that slice's backing array
	return failures
}

// RunOptions configures a RunWith call: an optional stable SagaID and Name for the store, an optional
// StateStore recording progress/outcome, and an optional RetryPolicy. The zero value (as Run uses) is
// a single in-process attempt with no recording.
type RunOptions struct {
	// SagaID is the saga instance id, stable across retry attempts and used in every store call.
	// Left empty, a new id is generated when a StateStore is present.
	SagaID string
	// Name is a human-readable saga name for grouping/reporting in the store. Defaults to "saga".
	Name string
	// StateStore, when set, records progress and outcome (nil records nothing).
	StateStore StateStore
	// RetryPolicy, when set, re-runs a cleanly-rolled-back saga up to its attempt limit (nil = one attempt).
	RetryPolicy *RetryPolicy
}

// RunInfo carries the identifying details of one saga run attempt, passed to a StateStore when the
// attempt starts.
type RunInfo struct {
	SagaID     string
	Name       string
	Attempt    int // 1-based
	StageCount int
}

// RetryPolicy is an optional whole-saga retry policy: after a CLEAN rollback, re-run the entire saga
// up to MaxAttempts times with exponential backoff. Retry is deliberately limited to
// OutcomeRolledBack - a succeeded saga needs none, and a partially-rolled-back one may have left
// effects a retry would double-apply, so it is surfaced for manual attention instead.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts (1 = no retry). A value below 1 is treated as 1.
	MaxAttempts int
	// InitialDelay is the delay before the second attempt (zero = no wait).
	InitialDelay time.Duration
	// BackoffFactor is the multiplier applied to the delay after each attempt (zero = 2.0).
	BackoffFactor float64

	// sleepFn is an overridable context-aware delay, for tests. nil = a real context-cancellable sleep.
	sleepFn func(context.Context, time.Duration)
}

func (p *RetryPolicy) maxAttempts() int {
	if p.MaxAttempts < 1 {
		return 1
	}
	return p.MaxAttempts
}

func (p *RetryPolicy) backoffFactor() float64 {
	if p.BackoffFactor == 0 {
		return 2.0
	}
	return p.BackoffFactor
}

func (p *RetryPolicy) sleep(ctx context.Context, d time.Duration) {
	if p.sleepFn != nil {
		p.sleepFn(ctx, d)
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
