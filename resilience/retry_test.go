package resilience

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// noSleep is an injected sleep that returns immediately, so a retry loop runs without real time.
func noSleep(context.Context, time.Duration) error { return nil }

// newIC builds a throwaway invocation context for driving the middleware directly.
func newIC() *benzene.InvocationContext {
	return benzene.NewInvocationContext(benzene.NewTopic("t"), nil, nil, nil)
}

func TestMiddleware_SuccessFirstAttemptDoesNotRetry(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error {
		calls++
		ic.Result = benzene.Ok(struct{}{})
		return nil
	}
	// Default retryOnResult never retries, so a first-attempt success runs next exactly once.
	if err := Middleware(WithSleep(noSleep))(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("next called %d times, want 1", calls)
	}
}

func TestMiddleware_RetryOnResultUntilSuccess(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error {
		calls++
		if calls <= 2 {
			ic.Result = benzene.ServiceUnavailable[any]("down")
		} else {
			ic.Result = benzene.Ok(struct{}{})
		}
		return nil
	}
	err := Middleware(WithRetryOnResult(RetryUnsuccessful), WithSleep(noSleep))(context.Background(), ic, next)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("next called %d times, want 3 (two failures then success)", calls)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("final status = %q, want ok", ic.Result.ResultStatus())
	}
}

func TestMiddleware_ExhaustsRetriesOnPersistentUnsuccessfulResult(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error {
		calls++
		ic.Result = benzene.ServiceUnavailable[any]("still down")
		return nil
	}
	err := Middleware(WithRetries(3), WithRetryOnResult(RetryUnsuccessful), WithSleep(noSleep))(context.Background(), ic, next)
	if err != nil {
		t.Fatalf("err = %v, want nil (retry gives up and returns the last result, not an error)", err)
	}
	if calls != 4 {
		t.Errorf("next called %d times, want 4 (initial + 3 retries)", calls)
	}
	if ic.Result.ResultStatus() != benzene.StatusServiceUnavailable {
		t.Errorf("final status = %q, want the last unsuccessful result to remain", ic.Result.ResultStatus())
	}
}

func TestMiddleware_BackoffGrowsAndCaps(t *testing.T) {
	var sleeps []time.Duration
	sleep := func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	ic := newIC()
	next := func(context.Context) error { ic.Result = benzene.ServiceUnavailable[any]("down"); return nil }

	Middleware(
		WithRetries(3),
		WithInitialDelay(100*time.Millisecond),
		WithBackoffFactor(2.0),
		WithMaxDelay(250*time.Millisecond),
		WithRetryOnResult(RetryUnsuccessful),
		WithSleep(sleep),
	)(context.Background(), ic, next)

	// 100, 200, then 400 capped to 250 - the cap applies to the sleep, not the growth curve.
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("sleep[%d] = %v, want %v", i, sleeps[i], want[i])
		}
	}
}

func TestMiddleware_JitterTransformsTheSleep(t *testing.T) {
	var sleeps []time.Duration
	sleep := func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	ic := newIC()
	next := func(context.Context) error { ic.Result = benzene.ServiceUnavailable[any]("down"); return nil }

	Middleware(
		WithRetries(1),
		WithInitialDelay(100*time.Millisecond),
		WithJitter(func(d time.Duration) time.Duration { return d / 2 }),
		WithRetryOnResult(RetryUnsuccessful),
		WithSleep(sleep),
	)(context.Background(), ic, next)

	if len(sleeps) != 1 || sleeps[0] != 50*time.Millisecond {
		t.Errorf("sleeps = %v, want [50ms] (jitter halved the 100ms delay)", sleeps)
	}
}

func TestMiddleware_UncappedWhenNoMaxDelay(t *testing.T) {
	var sleeps []time.Duration
	sleep := func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	ic := newIC()
	next := func(context.Context) error { ic.Result = benzene.ServiceUnavailable[any]("down"); return nil }

	Middleware(
		WithRetries(2),
		WithInitialDelay(100*time.Millisecond),
		WithBackoffFactor(3.0),
		WithRetryOnResult(RetryUnsuccessful),
		WithSleep(sleep),
	)(context.Background(), ic, next)

	want := []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}
	if len(sleeps) != len(want) || sleeps[0] != want[0] || sleeps[1] != want[1] {
		t.Errorf("sleeps = %v, want %v (uncapped growth)", sleeps, want)
	}
}

func TestMiddleware_RetryOnErrorThenSuccess(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error {
		calls++
		if calls <= 2 {
			return errors.New("transient boom")
		}
		ic.Result = benzene.Ok(struct{}{})
		return nil
	}
	// Default retryOnError retries any non-cancellation error.
	if err := Middleware(WithSleep(noSleep))(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil after a retry recovers", err)
	}
	if calls != 3 {
		t.Errorf("next called %d times, want 3", calls)
	}
}

func TestMiddleware_NonRetryableErrorReturnsImmediately(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error { calls++; return context.Canceled }
	err := Middleware(WithSleep(noSleep))(context.Background(), ic, next)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled propagated", err)
	}
	if calls != 1 {
		t.Errorf("next called %d times, want 1 (cancellation is not retried)", calls)
	}
}

func TestMiddleware_ErrorExhaustsRetriesAndPropagates(t *testing.T) {
	ic := newIC()
	calls := 0
	boom := errors.New("persistent boom")
	next := func(context.Context) error { calls++; return boom }
	err := Middleware(WithRetries(2), WithSleep(noSleep))(context.Background(), ic, next)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the persistent error propagated", err)
	}
	if calls != 3 {
		t.Errorf("next called %d times, want 3 (initial + 2 retries)", calls)
	}
}

func TestMiddleware_CustomRetryOnError(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error { calls++; return errors.New("nope") }
	// A predicate that never retries stops after the first attempt even for a non-cancel error.
	err := Middleware(WithRetryOnError(func(error) bool { return false }), WithSleep(noSleep))(context.Background(), ic, next)
	if err == nil || calls != 1 {
		t.Errorf("err = %v, calls = %d; want the error after exactly 1 attempt", err, calls)
	}
}

func TestMiddleware_SleepCancellationStopsRetrying(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error { calls++; ic.Result = benzene.ServiceUnavailable[any]("down"); return nil }
	sleep := func(context.Context, time.Duration) error { return context.Canceled }
	err := Middleware(WithRetryOnResult(RetryUnsuccessful), WithSleep(sleep))(context.Background(), ic, next)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled from the aborted backoff", err)
	}
	if calls != 1 {
		t.Errorf("next called %d times, want 1 (backoff cancelled before the retry)", calls)
	}
}

func TestRetryOnStatus(t *testing.T) {
	pred := RetryOnStatus(benzene.StatusServiceUnavailable, benzene.StatusTimeout)
	if !pred(benzene.ServiceUnavailable[any]("x")) {
		t.Error("service-unavailable should match")
	}
	if pred(benzene.BadRequest[any]("x")) {
		t.Error("bad-request should not match")
	}
	if pred(nil) {
		t.Error("nil result should not match")
	}
}

func TestRetryUnsuccessful(t *testing.T) {
	if RetryUnsuccessful(benzene.Ok(struct{}{})) {
		t.Error("a successful result should not be retried")
	}
	if !RetryUnsuccessful(benzene.ServiceUnavailable[any]("x")) {
		t.Error("an unsuccessful result should be retried")
	}
	if !RetryUnsuccessful(nil) {
		t.Error("a nil result should be retried (treated as not-successful)")
	}
}

// bareResult implements ResultInfo WITHOUT the optional ResultIsSuccessful, exercising
// resultSuccessful's status-class fallback.
type bareResult struct{ status benzene.Status }

func (b bareResult) ResultStatus() benzene.Status { return b.status }
func (b bareResult) ResultErrors() []string       { return nil }
func (b bareResult) ResultPayload() any           { return nil }

func TestResultSuccessful_StatusFallback(t *testing.T) {
	if !resultSuccessful(bareResult{benzene.StatusOk}) {
		t.Error("ok status should be successful via the fallback")
	}
	if resultSuccessful(bareResult{benzene.StatusBadRequest}) {
		t.Error("bad-request status should be unsuccessful via the fallback")
	}
}

func TestDefaultRetryOnError(t *testing.T) {
	if defaultRetryOnError(context.Canceled) {
		t.Error("context.Canceled should not be retried")
	}
	if defaultRetryOnError(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded should not be retried")
	}
	if !defaultRetryOnError(errors.New("boom")) {
		t.Error("an ordinary error should be retried")
	}
}

func TestGrowDelay(t *testing.T) {
	if got := growDelay(100*time.Millisecond, 2.0); got != 200*time.Millisecond {
		t.Errorf("growDelay = %v, want 200ms", got)
	}
	if got := growDelay(time.Duration(math.MaxInt64), 2.0); got != time.Duration(math.MaxInt64) {
		t.Errorf("growDelay overflow = %v, want clamp to MaxInt64", got)
	}
}

func TestFullJitter(t *testing.T) {
	seeded := FullJitter(rand.New(rand.NewSource(1)))
	for i := 0; i < 100; i++ {
		d := seeded(1000 * time.Millisecond)
		if d < 0 || d >= 1000*time.Millisecond {
			t.Fatalf("seeded jitter = %v, want [0, 1000ms)", d)
		}
	}
	// nil source uses the global (concurrency-safe) rand.
	if d := FullJitter(nil)(1000 * time.Millisecond); d < 0 || d >= 1000*time.Millisecond {
		t.Errorf("global jitter = %v, want [0, 1000ms)", d)
	}
	if d := FullJitter(nil)(0); d != 0 {
		t.Errorf("jitter of a non-positive delay = %v, want 0", d)
	}
}

func TestContextSleep(t *testing.T) {
	t.Run("non-positive returns immediately", func(t *testing.T) {
		if err := contextSleep(context.Background(), 0); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})
	t.Run("non-positive honors an already-cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := contextSleep(ctx, 0); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("positive completes", func(t *testing.T) {
		if err := contextSleep(context.Background(), time.Millisecond); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})
	t.Run("positive aborts on cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := contextSleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// TestMiddleware_DefaultSleepIsRealButBounded proves the default (non-injected) sleep path runs and
// respects a cancelled context without hanging.
func TestMiddleware_DefaultSleepRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ic := newIC()
	calls := 0
	next := func(context.Context) error { calls++; ic.Result = benzene.ServiceUnavailable[any]("down"); return nil }
	// No WithSleep: uses contextSleep. The cancelled ctx makes the first backoff abort.
	err := Middleware(WithRetryOnResult(RetryUnsuccessful), WithInitialDelay(time.Hour))(ctx, ic, next)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("next called %d times, want 1", calls)
	}
}
