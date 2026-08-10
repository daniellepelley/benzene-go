package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// Below the cap, every call runs the downstream and its result passes through untouched.
func TestBulkhead_UnderCapPassesThrough(t *testing.T) {
	ic := newIC()
	next := func(context.Context) error {
		ic.Result = benzene.Ok(struct{}{})
		return nil
	}

	if err := Bulkhead(2)(context.Background(), ic, next); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok", ic.Result.ResultStatus())
	}
}

// A next() error under the cap propagates unchanged - the bulkhead only limits concurrency, it does
// not alter a call it admitted.
func TestBulkhead_AdmittedErrorPropagates(t *testing.T) {
	ic := newIC()
	want := errors.New("boom")
	next := func(context.Context) error { return want }

	if err := Bulkhead(1)(context.Background(), ic, next); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// With no queue (the default), a call arriving while the single slot is busy is rejected fast with a
// too-many-requests result and never runs the downstream.
func TestBulkhead_FullRejectsFast(t *testing.T) {
	mw := Bulkhead(1)

	release := make(chan struct{})
	entered := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ic := newIC()
		_ = mw(context.Background(), ic, func(context.Context) error {
			close(entered)
			<-release // hold the only slot until the test releases it
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
	}()

	<-entered // the first call now holds the slot

	ic := newIC()
	ran := false
	err := mw(context.Background(), ic, func(context.Context) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil (a rejection is a result)", err)
	}
	if ran {
		t.Error("downstream ran, want it skipped while the bulkhead was full")
	}
	if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusTooManyRequests {
		t.Errorf("result = %v, want a too-many-requests rejection", ic.Result)
	}

	close(release)
	wg.Wait()
}

// After a busy call finishes and frees its slot, the next call is admitted again.
func TestBulkhead_SlotFreesAfterCompletion(t *testing.T) {
	mw := Bulkhead(1)

	ic1 := newIC()
	if err := mw(context.Background(), ic1, func(context.Context) error {
		ic1.Result = benzene.Ok(struct{}{})
		return nil
	}); err != nil {
		t.Fatalf("first call err = %v", err)
	}

	ic2 := newIC()
	ran := false
	if err := mw(context.Background(), ic2, func(context.Context) error {
		ran = true
		ic2.Result = benzene.Ok(struct{}{})
		return nil
	}); err != nil {
		t.Fatalf("second call err = %v", err)
	}
	if !ran {
		t.Error("second call was not admitted after the first freed its slot")
	}
}

// WithMaxQueue lets a caller WAIT for a busy slot instead of being rejected: the queued caller runs
// once the slot frees.
func TestBulkhead_QueuedCallerWaitsForSlot(t *testing.T) {
	mw := Bulkhead(1, WithMaxQueue(1))

	release := make(chan struct{})
	entered := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ic := newIC()
		_ = mw(context.Background(), ic, func(context.Context) error {
			close(entered)
			<-release
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
	}()
	<-entered // slot held

	queuedRan := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		ic := newIC()
		_ = mw(context.Background(), ic, func(context.Context) error {
			close(queuedRan)
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
	}()

	// The queued caller must not run while the slot is held.
	select {
	case <-queuedRan:
		t.Fatal("queued caller ran while the slot was still held")
	case <-time.After(20 * time.Millisecond):
	}

	close(release) // free the slot; the queued caller should now proceed
	select {
	case <-queuedRan:
	case <-time.After(time.Second):
		t.Fatal("queued caller did not run after the slot freed")
	}
	wg.Wait()
}

// With the running+waiting ceiling reached (cap 1 + queue 1 = 2 in flight), a third arrival is
// rejected fast.
func TestBulkhead_QueueCeilingRejects(t *testing.T) {
	mw := Bulkhead(1, WithMaxQueue(1))

	release := make(chan struct{})
	entered := make(chan struct{})
	var wg sync.WaitGroup

	// Caller 1: holds the only execution slot.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ic := newIC()
		_ = mw(context.Background(), ic, func(context.Context) error {
			close(entered)
			<-release
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
	}()
	<-entered

	// Caller 2: takes the single queue slot and blocks waiting for the execution slot.
	queuedWaiting := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		ic := newIC()
		close(queuedWaiting)
		_ = mw(context.Background(), ic, func(context.Context) error {
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
	}()
	<-queuedWaiting
	// Give caller 2 a moment to occupy the admission (queue) permit before we test the ceiling.
	time.Sleep(20 * time.Millisecond)

	// Caller 3: running+waiting ceiling reached, so it is rejected fast.
	ic := newIC()
	ran := false
	if err := mw(context.Background(), ic, func(context.Context) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ran {
		t.Error("third caller ran, want it rejected at the running+waiting ceiling")
	}
	if ic.Result == nil || ic.Result.ResultStatus() != benzene.StatusTooManyRequests {
		t.Errorf("result = %v, want a too-many-requests rejection", ic.Result)
	}

	close(release)
	wg.Wait()
}

// A queued caller whose context is cancelled while waiting stops waiting and surfaces the
// cancellation, without ever taking an execution slot.
func TestBulkhead_QueuedCancellationSurfaces(t *testing.T) {
	mw := Bulkhead(1, WithMaxQueue(1))

	release := make(chan struct{})
	entered := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ic := newIC()
		_ = mw(context.Background(), ic, func(context.Context) error {
			close(entered)
			<-release
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	ranc := make(chan bool, 1)
	go func() {
		ic := newIC()
		ran := false
		errc <- mw(ctx, ic, func(context.Context) error {
			ran = true
			return nil
		})
		ranc <- ran
	}()

	time.Sleep(20 * time.Millisecond) // let the queued caller block on the execution slot
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued caller did not return after cancellation")
	}
	if <-ranc {
		t.Error("downstream ran, want the cancelled caller to skip it")
	}

	close(release)
	wg.Wait()
}

// WithRejectStatus / WithRejectMessages customize the fail-fast rejection outcome.
func TestBulkhead_CustomRejection(t *testing.T) {
	mw := Bulkhead(1, WithRejectStatus(benzene.StatusServiceUnavailable), WithRejectMessages("at capacity"))

	release := make(chan struct{})
	entered := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ic := newIC()
		_ = mw(context.Background(), ic, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ic := newIC()
	if err := mw(context.Background(), ic, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusServiceUnavailable {
		t.Errorf("status = %q, want the custom service-unavailable", ic.Result.ResultStatus())
	}

	close(release)
	wg.Wait()
}

func TestBulkhead_InvalidConcurrencyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Bulkhead(0) did not panic")
		}
	}()
	Bulkhead(0)
}

func TestBulkhead_NegativeQueuePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Bulkhead with a negative queue did not panic")
		}
	}()
	Bulkhead(1, WithMaxQueue(-1))
}

func TestBulkhead_SuccessClassRejectStatusPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a success-class WithRejectStatus did not panic at wiring")
		}
	}()
	Bulkhead(1, WithRejectStatus(benzene.StatusOk))
}
