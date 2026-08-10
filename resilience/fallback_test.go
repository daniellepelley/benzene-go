package resilience

import (
	"context"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// A successful attempt does not trigger the fallback: its result passes through and the fallback
// never runs.
func TestFallback_SuccessDoesNotTrigger(t *testing.T) {
	ic := newIC()
	fallbackRan := false
	mw := Fallback(func(context.Context, *benzene.InvocationContext, error) error {
		fallbackRan = true
		return nil
	})

	err := mw(context.Background(), ic, func(context.Context) error {
		ic.Result = benzene.Ok(struct{}{})
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if fallbackRan {
		t.Error("fallback ran on a successful attempt")
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok", ic.Result.ResultStatus())
	}
}

// An unsuccessful result triggers the fallback (default FallbackUnsuccessful), which substitutes a
// new result; cause is nil because the trigger was a result, not an error.
func TestFallback_UnsuccessfulResultSubstituted(t *testing.T) {
	ic := newIC()
	var gotCause error
	mw := Fallback(func(_ context.Context, ic *benzene.InvocationContext, cause error) error {
		gotCause = cause
		ic.Result = benzene.Ok(struct{}{})
		return nil
	})

	err := mw(context.Background(), ic, func(context.Context) error {
		ic.Result = benzene.ServiceUnavailable[any]("down")
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if gotCause != nil {
		t.Errorf("cause = %v, want nil for a result-triggered fallback", gotCause)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want the substitute ok result", ic.Result.ResultStatus())
	}
}

// A next() error triggers the fallback, which receives that error as cause and substitutes a result;
// the middleware then returns nil (the fallback handled it).
func TestFallback_ErrorSubstituted(t *testing.T) {
	ic := newIC()
	boom := errors.New("boom")
	var gotCause error
	mw := Fallback(func(_ context.Context, ic *benzene.InvocationContext, cause error) error {
		gotCause = cause
		ic.Result = benzene.Ok(struct{}{})
		return nil
	})

	err := mw(context.Background(), ic, func(context.Context) error { return boom })
	if err != nil {
		t.Fatalf("err = %v, want nil (fallback handled it)", err)
	}
	if !errors.Is(gotCause, boom) {
		t.Errorf("cause = %v, want %v", gotCause, boom)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want the substitute ok result", ic.Result.ResultStatus())
	}
}

// A context cancellation is not a downstream failure to substitute for (default onError): it
// propagates unchanged and the fallback never runs.
func TestFallback_ContextCancellationNotSubstituted(t *testing.T) {
	ic := newIC()
	fallbackRan := false
	mw := Fallback(func(context.Context, *benzene.InvocationContext, error) error {
		fallbackRan = true
		return nil
	})

	err := mw(context.Background(), ic, func(context.Context) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled propagated", err)
	}
	if fallbackRan {
		t.Error("fallback ran on a context cancellation")
	}
}

// A fallback that itself returns an error propagates that error to the transport.
func TestFallback_FallbackErrorPropagates(t *testing.T) {
	ic := newIC()
	fbErr := errors.New("fallback failed")
	mw := Fallback(func(context.Context, *benzene.InvocationContext, error) error {
		return fbErr
	})

	err := mw(context.Background(), ic, func(context.Context) error {
		ic.Result = benzene.ServiceUnavailable[any]("down")
		return nil
	})
	if !errors.Is(err, fbErr) {
		t.Fatalf("err = %v, want the fallback's own error %v", err, fbErr)
	}
}

// FallbackOnStatus narrows the trigger to specific statuses; a non-matching unsuccessful result flows
// through untouched.
func TestFallback_OnStatusNarrowsTrigger(t *testing.T) {
	mw := Fallback(
		func(_ context.Context, ic *benzene.InvocationContext, _ error) error {
			ic.Result = benzene.Ok(struct{}{})
			return nil
		},
		WithFallbackOnResult(FallbackOnStatus(benzene.StatusServiceUnavailable)),
	)

	// A validation-error is not in the trigger set: it must flow through unchanged.
	ic := newIC()
	if err := mw(context.Background(), ic, func(context.Context) error {
		ic.Result = benzene.Fail[any](benzene.StatusValidationError, "bad")
		return nil
	}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusValidationError {
		t.Errorf("status = %q, want the validation-error preserved", ic.Result.ResultStatus())
	}

	// A service-unavailable IS in the trigger set: it is substituted.
	ic2 := newIC()
	if err := mw(context.Background(), ic2, func(context.Context) error {
		ic2.Result = benzene.ServiceUnavailable[any]("down")
		return nil
	}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic2.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want the substitute", ic2.Result.ResultStatus())
	}
}

// WithFallbackOnError customizes which errors trigger the fallback.
func TestFallback_OnErrorCustom(t *testing.T) {
	sentinel := errors.New("retryable")
	ran := false
	mw := Fallback(
		func(_ context.Context, ic *benzene.InvocationContext, _ error) error {
			ran = true
			ic.Result = benzene.Ok(struct{}{})
			return nil
		},
		WithFallbackOnError(func(err error) bool { return errors.Is(err, sentinel) }),
	)

	// Non-matching error: propagates, no fallback.
	ic := newIC()
	other := errors.New("other")
	if err := mw(context.Background(), ic, func(context.Context) error { return other }); !errors.Is(err, other) {
		t.Fatalf("err = %v, want %v propagated", err, other)
	}
	if ran {
		t.Error("fallback ran on a non-matching error")
	}

	// Matching error: fallback fires.
	ic2 := newIC()
	if err := mw(context.Background(), ic2, func(context.Context) error { return sentinel }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ran {
		t.Error("fallback did not fire on the matching error")
	}
}

// FallbackUnsuccessful treats a nil result as unsuccessful (so it triggers) - consistent with
// RetryUnsuccessful/TripUnsuccessful; FallbackOnStatus treats nil as no-match (a nil result has no
// status to classify against the set).
func TestFallback_NilResultPredicates(t *testing.T) {
	if !FallbackUnsuccessful(nil) {
		t.Error("FallbackUnsuccessful(nil) = false, want true (a nil result is not successful)")
	}
	if FallbackOnStatus(benzene.StatusServiceUnavailable)(nil) {
		t.Error("FallbackOnStatus(nil) = true, want false")
	}
}

func TestFallback_NilFallbackPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Fallback(nil) did not panic")
		}
	}()
	Fallback(nil)
}
