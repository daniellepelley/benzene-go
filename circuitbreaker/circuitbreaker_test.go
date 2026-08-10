package circuitbreaker

import (
	"context"
	"errors"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/sony/gobreaker/v2"
)

// newIC builds a throwaway invocation context for driving the middleware directly.
func newIC() *benzene.InvocationContext {
	return benzene.NewInvocationContext(benzene.NewTopic("t"), nil, nil, nil)
}

// tripAfter builds a real gobreaker that opens after n consecutive failures. Deterministic given the
// counts - no fake needed - which is the whole reason a circuit breaker is testable against the real
// library. A long Timeout keeps it open for the duration of a test unless we ask otherwise.
func tripAfter(n uint32, timeout time.Duration) *gobreaker.CircuitBreaker[struct{}] {
	return gobreaker.NewCircuitBreaker[struct{}](gobreaker.Settings{
		Name:        "test",
		Timeout:     timeout,
		ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= n },
	})
}

func TestMiddleware_PassesThroughWhileClosed(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error {
		calls++
		ic.Result = benzene.Ok(struct{}{})
		return nil
	}
	mw := Middleware(tripAfter(3, time.Hour))
	for i := 0; i < 5; i++ {
		if err := mw(context.Background(), ic, next); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	}
	if calls != 5 {
		t.Errorf("next called %d times, want 5 (breaker closed, every call passes through)", calls)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok", ic.Result.ResultStatus())
	}
}

func TestMiddleware_UnsuccessfulResultTripsThenShortCircuits(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error {
		calls++
		ic.Result = benzene.ServiceUnavailable[any]("down")
		return nil
	}
	mw := Middleware(tripAfter(3, time.Hour))

	// Three unsuccessful results trip the breaker; each ran the downstream and kept its own result
	// (not the fail-fast one), and none returned a Go error.
	for i := 0; i < 3; i++ {
		if err := mw(context.Background(), ic, next); err != nil {
			t.Fatalf("attempt %d err = %v, want nil (unsuccessful result is not a transport error)", i, err)
		}
		if ic.Result.ResultStatus() != benzene.StatusServiceUnavailable {
			t.Fatalf("attempt %d status = %q, want the downstream's service-unavailable", i, ic.Result.ResultStatus())
		}
	}
	if calls != 3 {
		t.Fatalf("next called %d times before tripping, want 3", calls)
	}

	// Now open: the next call must short-circuit WITHOUT touching the downstream.
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("open-state err = %v, want nil (short-circuit is a result, not an error)", err)
	}
	if calls != 3 {
		t.Errorf("next called %d times, want still 3 (open breaker must not invoke downstream)", calls)
	}
	if ic.Result.ResultStatus() != benzene.StatusServiceUnavailable {
		t.Errorf("open-state status = %q, want the default fail-fast service-unavailable", ic.Result.ResultStatus())
	}
	if errs := ic.Result.ResultErrors(); len(errs) != 1 || errs[0] != "circuit breaker is open" {
		t.Errorf("open-state errors = %v, want the default open message", errs)
	}
}

func TestMiddleware_DownstreamErrorCountsAndPropagates(t *testing.T) {
	ic := newIC()
	boom := errors.New("infra boom")
	calls := 0
	next := func(context.Context) error { calls++; return boom }
	mw := Middleware(tripAfter(2, time.Hour))

	// Two genuine errors: each propagates AND counts toward the trip threshold.
	for i := 0; i < 2; i++ {
		if err := mw(context.Background(), ic, next); !errors.Is(err, boom) {
			t.Fatalf("attempt %d err = %v, want the downstream error propagated", i, err)
		}
	}
	if calls != 2 {
		t.Fatalf("next called %d times, want 2", calls)
	}

	// Now open: short-circuit, no downstream call, and the error no longer propagates - it became a
	// fail-fast result instead.
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("open-state err = %v, want nil (now a fail-fast result, not the propagated error)", err)
	}
	if calls != 2 {
		t.Errorf("next called %d times, want still 2 (open breaker must not invoke downstream)", calls)
	}
	if ic.Result.ResultStatus() != benzene.StatusServiceUnavailable {
		t.Errorf("open-state status = %q, want service-unavailable", ic.Result.ResultStatus())
	}
}

func TestMiddleware_SuccessfulResultNeverTrips(t *testing.T) {
	ic := newIC()
	calls := 0
	next := func(context.Context) error {
		calls++
		ic.Result = benzene.Ok(struct{}{})
		return nil
	}
	// A breaker that would open on the very first failure - but a success must never produce one.
	mw := Middleware(tripAfter(1, time.Hour))
	for i := 0; i < 10; i++ {
		if err := mw(context.Background(), ic, next); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	}
	if calls != 10 {
		t.Errorf("next called %d times, want 10 (successful results never trip)", calls)
	}
}

func TestMiddleware_WithTripOnResult(t *testing.T) {
	ic := newIC()
	calls := 0
	// The downstream returns bad-request (unsuccessful), but we only trip on service-unavailable, so
	// the breaker must stay closed no matter how many bad-requests it sees.
	next := func(context.Context) error {
		calls++
		ic.Result = benzene.BadRequest[any]("nope")
		return nil
	}
	mw := Middleware(tripAfter(2, time.Hour), WithTripOnResult(TripOnStatus(benzene.StatusServiceUnavailable)))
	for i := 0; i < 5; i++ {
		if err := mw(context.Background(), ic, next); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if ic.Result.ResultStatus() != benzene.StatusBadRequest {
			t.Fatalf("status = %q, want bad-request to keep flowing through", ic.Result.ResultStatus())
		}
	}
	if calls != 5 {
		t.Errorf("next called %d times, want 5 (bad-request is not a tripping status here)", calls)
	}
}

func TestMiddleware_WithOpenStatusAndMessages(t *testing.T) {
	ic := newIC()
	next := func(context.Context) error { ic.Result = benzene.ServiceUnavailable[any]("down"); return nil }
	mw := Middleware(
		tripAfter(1, time.Hour),
		WithOpenStatus(benzene.StatusTimeout),
		WithOpenMessages("breaker tripped", "back off"),
	)

	// Trip it (one failure), then the short-circuit must carry the custom status and messages.
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("open-state err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusTimeout {
		t.Errorf("open-state status = %q, want the custom timeout", ic.Result.ResultStatus())
	}
	errs := ic.Result.ResultErrors()
	if len(errs) != 2 || errs[0] != "breaker tripped" || errs[1] != "back off" {
		t.Errorf("open-state errors = %v, want the custom messages", errs)
	}
}

func TestMiddleware_HalfOpenRecovery(t *testing.T) {
	ic := newIC()
	fail := true
	calls := 0
	next := func(context.Context) error {
		calls++
		if fail {
			ic.Result = benzene.ServiceUnavailable[any]("down")
		} else {
			ic.Result = benzene.Ok(struct{}{})
		}
		return nil
	}
	// Short Timeout so the open state elapses into half-open within the test; MaxRequests defaults to
	// 1, so a single successful probe closes the breaker again.
	mw := Middleware(tripAfter(1, 20*time.Millisecond))

	// Trip it.
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	callsAtTrip := calls

	// While open, a call short-circuits without invoking the downstream.
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("open-state err = %v, want nil", err)
	}
	if calls != callsAtTrip {
		t.Fatalf("downstream invoked while open (calls %d -> %d)", callsAtTrip, calls)
	}

	// Wait past the Timeout: the breaker moves to half-open and admits a probe.
	time.Sleep(60 * time.Millisecond)
	fail = false
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("half-open probe err = %v, want nil", err)
	}
	if calls != callsAtTrip+1 {
		t.Fatalf("half-open did not admit exactly one probe (calls %d -> %d)", callsAtTrip, calls)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Fatalf("probe status = %q, want ok (probe succeeded)", ic.Result.ResultStatus())
	}

	// The successful probe closed the breaker: traffic flows again.
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("closed-again err = %v, want nil", err)
	}
	if calls != callsAtTrip+2 {
		t.Errorf("breaker did not reclose after a successful probe (calls %d -> %d)", callsAtTrip, calls)
	}
}

func TestMiddleware_WithOpenStatusRejectsSuccessStatusAtWiring(t *testing.T) {
	// A success-class open status is a wiring mistake: it must panic at construction (benzene.Fail's
	// contract), never per-request where it would reach the transport.
	defer func() {
		if recover() == nil {
			t.Error("Middleware with a success-class open status should panic at wiring")
		}
	}()
	Middleware(tripAfter(1, time.Hour), WithOpenStatus(benzene.StatusOk))
}

func TestTripUnsuccessful(t *testing.T) {
	if TripUnsuccessful(benzene.Ok(struct{}{})) {
		t.Error("a successful result should not trip")
	}
	if !TripUnsuccessful(benzene.ServiceUnavailable[any]("x")) {
		t.Error("an unsuccessful result should trip")
	}
	if !TripUnsuccessful(nil) {
		t.Error("a nil result should trip (treated as not-successful)")
	}
}

func TestTripOnStatus(t *testing.T) {
	pred := TripOnStatus(benzene.StatusServiceUnavailable, benzene.StatusTimeout)
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
