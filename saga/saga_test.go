package saga

import (
	"context"
	"sync"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// recorder captures the order of forward/compensation effects so a test can assert LIFO rollback.
type recorder struct {
	mu  sync.Mutex
	log []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, s)
}

func (r *recorder) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

// okStep is a step that succeeds, recording its forward and compensation.
func okStep(rec *recorder, name string) Step {
	return NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] {
			rec.add("do:" + name)
			return benzene.Ok(name)
		},
		func(context.Context, *SagaContext, string) benzene.Result[struct{}] {
			rec.add("undo:" + name)
			return benzene.Ok(struct{}{})
		},
	)
}

func failStep(rec *recorder, name string) Step {
	return NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] {
			rec.add("do-fail:" + name)
			return benzene.ServiceUnavailable[string]("boom")
		},
		func(context.Context, *SagaContext, string) benzene.Result[struct{}] {
			rec.add("undo:" + name)
			return benzene.Ok(struct{}{})
		},
	)
}

func TestSaga_AllStagesSucceed(t *testing.T) {
	rec := &recorder{}
	s := New(
		NewStage(okStep(rec, "a")),
		NewStage(okStep(rec, "b")),
	)
	res := s.Run(context.Background())
	if !res.IsSuccess() || res.Outcome != OutcomeSucceeded {
		t.Fatalf("Outcome = %q, want succeeded", res.Outcome)
	}
	if res.FailedStageIndex != -1 {
		t.Errorf("FailedStageIndex = %d, want -1 on success", res.FailedStageIndex)
	}
	if got := rec.events(); len(got) != 2 || got[0] != "do:a" || got[1] != "do:b" {
		t.Errorf("events = %v, want [do:a do:b] with no compensation", got)
	}
}

func TestSaga_ContextThreadsBetweenStages(t *testing.T) {
	var seen string
	s := New(
		NewStage(NewStep(
			func(_ context.Context, sc *SagaContext) benzene.Result[string] {
				Set(sc, "tenant-42", "tenant")
				return benzene.Ok("s1")
			}, nil)),
		NewStage(NewStep(
			func(_ context.Context, sc *SagaContext) benzene.Result[string] {
				v, ok := Get[string](sc, "tenant")
				if !ok {
					return benzene.BadRequest[string]("missing tenant")
				}
				seen = v
				return benzene.Ok("s2")
			}, nil)),
	)
	if res := s.Run(context.Background()); !res.IsSuccess() {
		t.Fatalf("Outcome = %q, want succeeded", res.Outcome)
	}
	if seen != "tenant-42" {
		t.Errorf("stage 2 saw tenant %q, want the value stage 1 published", seen)
	}
}

func TestSaga_StageFailureRollsBackLIFO(t *testing.T) {
	rec := &recorder{}
	s := New(
		NewStage(okStep(rec, "a")),
		NewStage(okStep(rec, "b")),
		NewStage(failStep(rec, "c")),
	)
	res := s.Run(context.Background())
	if res.Outcome != OutcomeRolledBack {
		t.Fatalf("Outcome = %q, want rolled-back", res.Outcome)
	}
	if res.FailedStageIndex != 2 {
		t.Errorf("FailedStageIndex = %d, want 2", res.FailedStageIndex)
	}
	if res.Failure == nil || res.Failure.ResultStatus() != benzene.StatusServiceUnavailable {
		t.Errorf("Failure = %v, want the failing step's service-unavailable result", res.Failure)
	}
	// Forward order a,b,c(fail); rollback undoes newest-first: the failed stage (c has nothing to
	// undo since it failed), then b, then a.
	want := []string{"do:a", "do:b", "do-fail:c", "undo:b", "undo:a"}
	got := rec.events()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("events = %v, want %v", got, want)
			break
		}
	}
}

func TestSaga_ConcurrentStepFailureCompensatesSucceededSibling(t *testing.T) {
	rec := &recorder{}
	// One stage with two concurrent steps: one succeeds, one fails -> the stage fails and the
	// succeeded sibling is compensated.
	s := New(NewStage(okStep(rec, "ok"), failStep(rec, "bad")))
	res := s.Run(context.Background())
	if res.Outcome != OutcomeRolledBack {
		t.Fatalf("Outcome = %q, want rolled-back", res.Outcome)
	}
	events := rec.events()
	var undidOK bool
	for _, e := range events {
		if e == "undo:ok" {
			undidOK = true
		}
	}
	if !undidOK {
		t.Errorf("events = %v, want the succeeded sibling 'ok' compensated", events)
	}
}

func TestSaga_StepWithNoCompensationIsNothingToUndo(t *testing.T) {
	rec := &recorder{}
	noComp := NewStep(func(context.Context, *SagaContext) benzene.Result[string] {
		rec.add("do:x")
		return benzene.Ok("x")
	}, nil)
	s := New(NewStage(noComp), NewStage(failStep(rec, "y")))
	res := s.Run(context.Background())
	if res.Outcome != OutcomeRolledBack {
		t.Fatalf("Outcome = %q, want rolled-back (a compensationless succeeded step is clean)", res.Outcome)
	}
	if len(res.CompensationFailures) != 0 {
		t.Errorf("CompensationFailures = %v, want none", res.CompensationFailures)
	}
}

func TestSaga_CompensationFailureIsPartiallyRolledBack(t *testing.T) {
	badComp := NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] { return benzene.Ok("a") },
		func(context.Context, *SagaContext, string) benzene.Result[struct{}] {
			return benzene.ServiceUnavailable[struct{}]("undo failed")
		},
	)
	s := New(NewStage(badComp), NewStage(NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] { return benzene.BadRequest[string]("nope") }, nil)))
	res := s.Run(context.Background())
	if res.Outcome != OutcomePartiallyRolledBack {
		t.Fatalf("Outcome = %q, want partially-rolled-back", res.Outcome)
	}
	if len(res.CompensationFailures) != 1 || res.CompensationFailures[0].State() != StepCompensationFailed {
		t.Errorf("CompensationFailures = %v, want one compensation-failed step", res.CompensationFailures)
	}
}

func TestSaga_ForwardPanicIsFailedStep(t *testing.T) {
	s := New(NewStage(NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] { panic("kaboom") }, nil)))
	res := s.Run(context.Background())
	if res.Outcome != OutcomeRolledBack {
		t.Fatalf("Outcome = %q, want rolled-back after a forward panic", res.Outcome)
	}
	if res.FailureErr == nil {
		t.Errorf("FailureErr = nil, want the recovered panic")
	}
	if res.Failure == nil || res.Failure.ResultStatus() != benzene.StatusUnexpectedError {
		t.Errorf("Failure = %v, want unexpected-error for a panic", res.Failure)
	}
}

func TestSaga_CompensationPanicIsCompensationFailed(t *testing.T) {
	panicComp := NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] { return benzene.Ok("a") },
		func(context.Context, *SagaContext, string) benzene.Result[struct{}] { panic("undo kaboom") },
	)
	s := New(NewStage(panicComp), NewStage(NewStep(
		func(context.Context, *SagaContext) benzene.Result[string] { return benzene.BadRequest[string]("nope") }, nil)))
	res := s.Run(context.Background())
	if res.Outcome != OutcomePartiallyRolledBack {
		t.Fatalf("Outcome = %q, want partially-rolled-back after a compensation panic", res.Outcome)
	}
	if len(res.CompensationFailures) != 1 || res.CompensationFailures[0].Err() == nil {
		t.Errorf("want one compensation-failed step carrying the recovered panic; got %v", res.CompensationFailures)
	}
}

func TestStep_StateProgression(t *testing.T) {
	st := okStep(&recorder{}, "a")
	if st.State() != StepPending {
		t.Errorf("initial State = %q, want pending", st.State())
	}
	if st.Result() != nil {
		t.Errorf("initial Result = %v, want nil before running", st.Result())
	}
	sc := newSagaContext()
	st.execute(context.Background(), sc)
	if st.State() != StepSucceeded {
		t.Errorf("State = %q, want succeeded after a successful forward", st.State())
	}
}

func TestStep_PayloadNilSafe(t *testing.T) {
	// A succeeded step whose result carries no payload pointer must not panic on publish.
	st := &step[string]{state: StepSucceeded, hasResult: true, result: benzene.Result[string]{}}
	if got := st.payload(); got != "" {
		t.Errorf("payload() = %q, want the zero value for a nil payload", got)
	}
	sc := newSagaContext()
	st.publish(sc) // must not panic
	if v, ok := Get[string](sc); !ok || v != "" {
		t.Errorf("published value = %q,%v, want the zero value stored", v, ok)
	}
}

func TestSagaContext_TypedAndKeyed(t *testing.T) {
	sc := newSagaContext()
	Set(sc, 42)              // typed
	Set(sc, "x", "explicit") // keyed
	if v, ok := Get[int](sc); !ok || v != 42 {
		t.Errorf("Get[int] = %v,%v, want 42,true", v, ok)
	}
	if v, ok := Get[string](sc, "explicit"); !ok || v != "x" {
		t.Errorf("Get keyed = %v,%v, want x,true", v, ok)
	}
	if !Has[int](sc) || !Has[string](sc, "explicit") {
		t.Errorf("Has should report both present")
	}
	if _, ok := Get[float64](sc); ok {
		t.Errorf("Get of an absent type should be false")
	}
	if _, ok := Get[int](sc, "explicit"); ok {
		t.Errorf("Get[int] under the string key should be false (wrong type)")
	}
}

func TestNewSagaID_UniqueHex(t *testing.T) {
	a, b := newSagaID(), newSagaID()
	if len(a) != 32 || len(b) != 32 {
		t.Errorf("ids = %q,%q, want 32 hex chars each", a, b)
	}
	if a == b {
		t.Errorf("two ids were equal (%q); want distinct", a)
	}
}
