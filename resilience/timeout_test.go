package resilience

import (
	"context"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// A handler that honors its context is bounded by the deadline and its outcome is presented as a
// StatusTimeout result; the middleware returns nil (the timeout is a result, not a transport error).
func TestTimeout_DeadlineHonoringHandlerBecomesTimeoutResult(t *testing.T) {
	ic := newIC()
	next := func(c context.Context) error {
		<-c.Done() // a well-behaved handler returns when its context is cancelled
		return c.Err()
	}

	err := Timeout(5*time.Millisecond)(context.Background(), ic, next)

	if err != nil {
		t.Fatalf("Timeout middleware returned error %v, want nil (timeout is a result)", err)
	}
	if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusTimeout {
		t.Errorf("result = %v, want a StatusTimeout result", ic.Result)
	}
}

// A handler that finishes within the deadline keeps its result untouched.
func TestTimeout_FastSuccessIsPreserved(t *testing.T) {
	ic := newIC()
	next := func(context.Context) error {
		ic.Result = benzene.Ok(struct{}{})
		return nil
	}

	err := Timeout(time.Hour)(context.Background(), ic, next)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok (a fast success is preserved)", ic.Result.ResultStatus())
	}
}

// A fast unsuccessful result (no deadline hit) is left as-is, not relabeled as a timeout.
func TestTimeout_FastFailureIsPreserved(t *testing.T) {
	ic := newIC()
	next := func(context.Context) error {
		ic.Result = benzene.ServiceUnavailable[any]("down")
		return nil
	}

	if err := Timeout(time.Hour)(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusServiceUnavailable {
		t.Errorf("status = %q, want the original failure preserved", ic.Result.ResultStatus())
	}
}

// A parent-context cancellation (the caller giving up, not our deadline) is surfaced as-is, never
// relabeled as a handler timeout.
func TestTimeout_ParentCancellationNotRelabeled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller has already given up

	ic := newIC()
	next := func(c context.Context) error { return c.Err() }

	err := Timeout(time.Hour)(ctx, ic, next)

	if err == nil {
		t.Fatal("want the parent cancellation surfaced as an error, got nil")
	}
	if ic.Result != nil && ic.Result.ResultStatus() == benzene.StatusTimeout {
		t.Error("a parent cancellation must not become a StatusTimeout")
	}
}

// The deadline elapsed but the handler still produced a success right at the boundary: the success
// is preserved (err nil + successful result), not clobbered by a timeout.
func TestTimeout_SuccessAtBoundaryIsNotClobbered(t *testing.T) {
	ic := newIC()
	next := func(context.Context) error {
		time.Sleep(2 * time.Millisecond) // ensure the 1ns deadline has already elapsed
		ic.Result = benzene.Ok(struct{}{})
		return nil
	}

	if err := Timeout(time.Nanosecond)(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok preserved even though the deadline elapsed", ic.Result.ResultStatus())
	}
}

// The deadline elapsed and the handler produced an unsuccessful result: it is presented as a timeout.
func TestTimeout_UnsuccessfulAfterDeadlineBecomesTimeout(t *testing.T) {
	ic := newIC()
	next := func(context.Context) error {
		time.Sleep(2 * time.Millisecond)
		ic.Result = benzene.ServiceUnavailable[any]("slow dependency")
		return nil
	}

	if err := Timeout(time.Nanosecond)(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusTimeout {
		t.Errorf("status = %q, want a timeout once the deadline elapsed on an unsuccessful attempt", ic.Result.ResultStatus())
	}
}

// The deadline elapsed and the handler returned an error: presented as a timeout.
func TestTimeout_ErrorAfterDeadlineBecomesTimeout(t *testing.T) {
	ic := newIC()
	next := func(c context.Context) error {
		<-c.Done()
		return c.Err()
	}

	if err := Timeout(time.Millisecond)(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusTimeout {
		t.Errorf("result = %v, want StatusTimeout", ic.Result)
	}
}
