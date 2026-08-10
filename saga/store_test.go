package saga

import (
	"context"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// twoStageOK is a saga that succeeds (two stages, one step each).
func twoStageOK() *Saga {
	step := func() Step {
		return NewStep(func(context.Context, *SagaContext) benzene.Result[string] { return benzene.Ok("ok") }, nil)
	}
	return New(NewStage(step()), NewStage(step()))
}

func TestRunWith_StoreRecordsProgress(t *testing.T) {
	store := NewInMemoryStateStore()
	res := twoStageOK().RunWith(context.Background(), RunOptions{SagaID: "s-1", Name: "checkout", StateStore: store})
	if !res.IsSuccess() {
		t.Fatalf("Outcome = %q, want succeeded", res.Outcome)
	}

	events := store.EventsFor("s-1")
	// started, stage 0 completed, stage 1 completed, finished.
	if len(events) != 4 {
		t.Fatalf("events = %+v, want 4 (started, 2x stage-completed, finished)", events)
	}
	if events[0].Kind != EventStarted {
		t.Errorf("event[0].Kind = %q, want started", events[0].Kind)
	}
	if events[1].Kind != EventStageCompleted || events[1].StageIndex != 0 {
		t.Errorf("event[1] = %+v, want stage-completed 0", events[1])
	}
	if events[2].StageIndex != 1 {
		t.Errorf("event[2].StageIndex = %d, want 1", events[2].StageIndex)
	}
	if events[3].Kind != EventFinished || events[3].Result.Outcome != OutcomeSucceeded {
		t.Errorf("event[3] = %+v, want finished/succeeded", events[3])
	}
}

func TestRunWith_GeneratesSagaIDWhenStorePresent(t *testing.T) {
	store := NewInMemoryStateStore()
	twoStageOK().RunWith(context.Background(), RunOptions{StateStore: store})
	events := store.Events()
	if len(events) == 0 || events[0].SagaID == "" {
		t.Fatalf("events = %+v, want a generated non-empty saga id", events)
	}
	id := events[0].SagaID
	for _, e := range events {
		if e.SagaID != id {
			t.Errorf("event id %q differs from %q; want one stable id across the run", e.SagaID, id)
		}
	}
}

func TestRunWith_NameDefaultsToSaga(t *testing.T) {
	var gotName string
	store := recordingStore{onStart: func(run RunInfo) { gotName = run.Name }}
	twoStageOK().RunWith(context.Background(), RunOptions{SagaID: "s", StateStore: store})
	if gotName != "saga" {
		t.Errorf("RunInfo.Name = %q, want the default \"saga\"", gotName)
	}
}

// recordingStore is a minimal StateStore capturing the RunInfo, for asserting defaults.
type recordingStore struct {
	onStart func(RunInfo)
}

func (r recordingStore) RecordStarted(_ context.Context, run RunInfo)         { r.onStart(run) }
func (recordingStore) RecordStageCompleted(context.Context, string, int, int) {}
func (recordingStore) RecordFinished(context.Context, string, int, Result)    {}

// rollbackOnce builds a saga that fails its second stage (clean rollback) until the shared attempt
// counter reaches succeedOn, after which it succeeds. Each Run increments *attempts.
func rollbackOnce(attempts *int, succeedOn int) *Saga {
	stage1 := NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] { return benzene.Ok("a") },
		func(context.Context, *SagaContext, string) benzene.Result[struct{}] { return benzene.Ok(struct{}{}) },
	)
	stage2 := NewStep(func(context.Context, *SagaContext) benzene.Result[string] {
		*attempts++
		if *attempts >= succeedOn {
			return benzene.Ok("done")
		}
		return benzene.ServiceUnavailable[string]("retry me")
	}, nil)
	return New(NewStage(stage1), NewStage(stage2))
}

func TestRetry_CleanRollbackRetriedThenSucceeds(t *testing.T) {
	attempts := 0
	s := rollbackOnce(&attempts, 2) // fail once, succeed on the 2nd attempt
	res := s.RunWith(context.Background(), RunOptions{
		RetryPolicy: &RetryPolicy{MaxAttempts: 3, sleepFn: func(context.Context, time.Duration) {}},
	})
	if !res.IsSuccess() {
		t.Fatalf("Outcome = %q, want succeeded on retry", res.Outcome)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one clean rollback, then success)", attempts)
	}
}

func TestRetry_ExhaustsAttemptsAndReturnsRolledBack(t *testing.T) {
	attempts := 0
	s := rollbackOnce(&attempts, 99) // never succeeds
	res := s.RunWith(context.Background(), RunOptions{
		RetryPolicy: &RetryPolicy{MaxAttempts: 3, sleepFn: func(context.Context, time.Duration) {}},
	})
	if res.Outcome != OutcomeRolledBack {
		t.Fatalf("Outcome = %q, want rolled-back after exhausting retries", res.Outcome)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (MaxAttempts)", attempts)
	}
}

func TestRetry_BackoffGrowsWithDefaultFactor(t *testing.T) {
	var delays []time.Duration
	attempts := 0
	s := rollbackOnce(&attempts, 99)
	s.RunWith(context.Background(), RunOptions{
		RetryPolicy: &RetryPolicy{
			MaxAttempts:  3,
			InitialDelay: 100 * time.Millisecond,
			// BackoffFactor unset -> defaults to 2.0.
			sleepFn: func(_ context.Context, d time.Duration) { delays = append(delays, d) },
		},
	})
	// Two sleeps (before attempts 2 and 3); the third attempt is the last, so no sleep after it.
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(delays) != len(want) || delays[0] != want[0] || delays[1] != want[1] {
		t.Errorf("delays = %v, want %v (default factor 2.0)", delays, want)
	}
}

func TestRetry_PartiallyRolledBackIsNotRetried(t *testing.T) {
	attempts := 0
	badComp := NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] { return benzene.Ok("a") },
		func(context.Context, *SagaContext, string) benzene.Result[struct{}] {
			return benzene.ServiceUnavailable[struct{}]("undo failed")
		},
	)
	failing := NewStep(func(context.Context, *SagaContext) benzene.Result[string] {
		attempts++
		return benzene.BadRequest[string]("nope")
	}, nil)
	s := New(NewStage(badComp), NewStage(failing))

	res := s.RunWith(context.Background(), RunOptions{
		RetryPolicy: &RetryPolicy{MaxAttempts: 3, sleepFn: func(context.Context, time.Duration) {}},
	})
	if res.Outcome != OutcomePartiallyRolledBack {
		t.Fatalf("Outcome = %q, want partially-rolled-back", res.Outcome)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a partial rollback must not be retried)", attempts)
	}
}

func TestRetry_SucceededRunsOnce(t *testing.T) {
	store := NewInMemoryStateStore()
	res := twoStageOK().RunWith(context.Background(), RunOptions{
		SagaID:      "s",
		StateStore:  store,
		RetryPolicy: &RetryPolicy{MaxAttempts: 5, sleepFn: func(context.Context, time.Duration) {}},
	})
	if !res.IsSuccess() {
		t.Fatalf("Outcome = %q, want succeeded", res.Outcome)
	}
	// One finished event -> exactly one attempt.
	finished := 0
	for _, e := range store.Events() {
		if e.Kind == EventFinished {
			finished++
		}
	}
	if finished != 1 {
		t.Errorf("finished events = %d, want 1 (a success is not retried)", finished)
	}
}

func TestRunWith_StoreRecordsFailureOutcome(t *testing.T) {
	store := NewInMemoryStateStore()
	s := New(NewStage(NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] {
			return benzene.ServiceUnavailable[string]("down")
		}, nil)))
	res := s.RunWith(context.Background(), RunOptions{SagaID: "f-1", StateStore: store})
	if res.Outcome != OutcomeRolledBack {
		t.Fatalf("Outcome = %q, want rolled-back", res.Outcome)
	}
	events := store.EventsFor("f-1")
	last := events[len(events)-1]
	if last.Kind != EventFinished || last.Result.Outcome != OutcomeRolledBack {
		t.Errorf("final event = %+v, want finished/rolled-back recorded to the store", last)
	}
}

func TestRetry_ExplicitBackoffFactor(t *testing.T) {
	var delays []time.Duration
	attempts := 0
	s := rollbackOnce(&attempts, 99)
	s.RunWith(context.Background(), RunOptions{
		RetryPolicy: &RetryPolicy{
			MaxAttempts:   3,
			InitialDelay:  100 * time.Millisecond,
			BackoffFactor: 3.0,
			sleepFn:       func(_ context.Context, d time.Duration) { delays = append(delays, d) },
		},
	})
	want := []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}
	if len(delays) != len(want) || delays[0] != want[0] || delays[1] != want[1] {
		t.Errorf("delays = %v, want %v (explicit factor 3.0)", delays, want)
	}
}

func TestNewKeyedStep_PublishesUnderExplicitKey(t *testing.T) {
	var saw string
	s := New(
		NewStage(NewKeyedStep("primary",
			func(context.Context, *SagaContext) benzene.Result[string] { return benzene.Ok("v1") }, nil)),
		NewStage(NewStep(func(_ context.Context, sc *SagaContext) benzene.Result[string] {
			v, _ := Get[string](sc, "primary")
			saw = v
			return benzene.Ok("v2")
		}, nil)),
	)
	if res := s.Run(context.Background()); !res.IsSuccess() {
		t.Fatalf("Outcome = %q, want succeeded", res.Outcome)
	}
	if saw != "v1" {
		t.Errorf("stage 2 read %q under the explicit key, want v1", saw)
	}
}

func TestRetry_MaxAttemptsBelowOneIsOne(t *testing.T) {
	attempts := 0
	s := rollbackOnce(&attempts, 99)
	s.RunWith(context.Background(), RunOptions{RetryPolicy: &RetryPolicy{MaxAttempts: 0}})
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (MaxAttempts below 1 is treated as 1)", attempts)
	}
}

func TestRetryPolicy_DefaultSleepRunsAndCancels(t *testing.T) {
	t.Run("real sleep elapses", func(t *testing.T) {
		attempts := 0
		s := rollbackOnce(&attempts, 2)
		res := s.RunWith(context.Background(), RunOptions{
			RetryPolicy: &RetryPolicy{MaxAttempts: 2, InitialDelay: time.Millisecond}, // no sleepFn -> real timer
		})
		if !res.IsSuccess() {
			t.Errorf("Outcome = %q, want succeeded after a real 1ms backoff", res.Outcome)
		}
	})
	t.Run("cancelled context aborts the wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p := &RetryPolicy{}
		done := make(chan struct{})
		go func() { p.sleep(ctx, time.Hour); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("sleep did not return promptly on a cancelled context")
		}
	})
}
