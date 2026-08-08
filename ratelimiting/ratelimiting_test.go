package ratelimiting

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// fakeLimiter grants or rejects on command and records the permit cost it was asked for and whether
// its release ran.
type fakeLimiter struct {
	grant    bool
	gotCost  int
	released bool
}

func (f *fakeLimiter) Allow(cost int) (func(), bool) {
	f.gotCost = cost
	if !f.grant {
		return func() {}, false
	}
	return func() { f.released = true }, true
}

func runMiddleware(t *testing.T, limiter Limiter, cost CostFunc, next func(context.Context) error) *benzene.InvocationContext {
	t.Helper()
	ic := benzene.NewInvocationContext(benzene.NewTopic("t"), nil, nil, nil)
	if err := Middleware(limiter, cost)(context.Background(), ic, next); err != nil {
		t.Fatalf("middleware error = %v", err)
	}
	return ic
}

func TestMiddleware_AllowedRunsHandler(t *testing.T) {
	ran := false
	ic := runMiddleware(t, &fakeLimiter{grant: true}, OnePerMessage, func(context.Context) error {
		ran = true
		return nil
	})
	if !ran {
		t.Error("handler did not run when the limiter allowed the message")
	}
	if ic.Result != nil {
		t.Errorf("result = %+v, want nil (the limiter did not short-circuit)", ic.Result)
	}
}

func TestMiddleware_RejectedShortCircuitsToTooManyRequests(t *testing.T) {
	ran := false
	ic := runMiddleware(t, &fakeLimiter{grant: false}, OnePerMessage, func(context.Context) error {
		ran = true
		return nil
	})
	if ran {
		t.Error("handler ran despite the limiter rejecting the message")
	}
	if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusTooManyRequests {
		t.Errorf("result = %+v, want too-many-requests", ic.Result)
	}
}

func TestMiddleware_LeaseHeldAcrossNextAndReleased(t *testing.T) {
	limiter := &fakeLimiter{grant: true}
	releasedDuringNext := false
	runMiddleware(t, limiter, OnePerMessage, func(context.Context) error {
		// The lease must still be held while the handler runs.
		releasedDuringNext = limiter.released
		return nil
	})
	if releasedDuringNext {
		t.Error("lease was released before the handler finished")
	}
	if !limiter.released {
		t.Error("lease was not released after the handler finished")
	}
}

func TestMiddleware_NegativeCostClampedToZero(t *testing.T) {
	limiter := &fakeLimiter{grant: true}
	runMiddleware(t, limiter, func(*benzene.InvocationContext) int { return -5 }, func(context.Context) error { return nil })
	if limiter.gotCost != 0 {
		t.Errorf("cost passed to limiter = %d, want 0 (a negative cost is clamped)", limiter.gotCost)
	}
}

func TestMiddleware_CostFuncValueIsPassedThrough(t *testing.T) {
	limiter := &fakeLimiter{grant: true}
	runMiddleware(t, limiter, func(*benzene.InvocationContext) int { return 7 }, func(context.Context) error { return nil })
	if limiter.gotCost != 7 {
		t.Errorf("cost = %d, want 7", limiter.gotCost)
	}
}

// nilReleaseLimiter grants but returns a nil release, to prove the middleware guards against a
// misbehaving Limiter rather than panicking during the deferred unwind.
type nilReleaseLimiter struct{}

func (nilReleaseLimiter) Allow(int) (func(), bool) { return nil, true }

func TestMiddleware_NilReleaseFromLimiterDoesNotPanic(t *testing.T) {
	ran := false
	ic := runMiddleware(t, nilReleaseLimiter{}, OnePerMessage, func(context.Context) error {
		ran = true
		return nil
	})
	if !ran {
		t.Error("handler did not run")
	}
	if ic.Result != nil {
		t.Errorf("result = %+v, want nil (allowed)", ic.Result)
	}
}

func TestOnePerMessage(t *testing.T) {
	if got := OnePerMessage(nil); got != 1 {
		t.Errorf("OnePerMessage = %d, want 1", got)
	}
}
