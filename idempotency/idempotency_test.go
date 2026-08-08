package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// runner wires a pipeline of the idempotency middleware in front of a router, with a handler that
// counts how many times it actually ran and can be told to fail.
type runner struct {
	pipeline *benzene.Pipeline
	ran      int
	fail     bool
}

func newRunner(store Store) *runner {
	r := &runner{}
	registry := benzene.NewRegistry()
	_ = benzene.Register(registry, benzene.NewTopic("order:create"), benzene.Handler[map[string]any, struct{}](
		func(context.Context, map[string]any) benzene.Result[struct{}] {
			r.ran++
			if r.fail {
				return benzene.ServiceUnavailable[struct{}]("downstream down")
			}
			return benzene.Ok(struct{}{})
		}))
	r.pipeline = benzene.NewPipeline(Middleware(store, HeaderKey(DefaultKeyHeader)), benzene.RouterMiddleware(registry))
	return r
}

func (r *runner) send(t *testing.T, headers map[string]string) *benzene.InvocationContext {
	t.Helper()
	ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), headers, json.RawMessage(`{}`), benzene.NewContainer().NewScope())
	if err := r.pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("pipeline.Run() error = %v", err)
	}
	return ic
}

func withKey(k string) map[string]string { return map[string]string{DefaultKeyHeader: k} }

func TestMiddleware_FirstProcessesDuplicateIgnored(t *testing.T) {
	r := newRunner(NewInMemoryStore())

	first := r.send(t, withKey("k1"))
	if first.Result.ResultStatus() != benzene.StatusOk {
		t.Fatalf("first status = %q, want ok", first.Result.ResultStatus())
	}

	second := r.send(t, withKey("k1"))
	if second.Result.ResultStatus() != benzene.StatusIgnored {
		t.Errorf("duplicate status = %q, want ignored", second.Result.ResultStatus())
	}
	if r.ran != 1 {
		t.Errorf("handler ran %d times, want 1 - the duplicate must be short-circuited", r.ran)
	}
}

func TestMiddleware_NoKeyProcessesEveryTime(t *testing.T) {
	r := newRunner(NewInMemoryStore())

	r.send(t, nil)
	r.send(t, nil)
	if r.ran != 2 {
		t.Errorf("handler ran %d times, want 2 - a message with no key opts out of dedup", r.ran)
	}
}

func TestMiddleware_DifferentKeysBothProcess(t *testing.T) {
	r := newRunner(NewInMemoryStore())
	r.send(t, withKey("k1"))
	r.send(t, withKey("k2"))
	if r.ran != 2 {
		t.Errorf("handler ran %d times, want 2 for two distinct keys", r.ran)
	}
}

func TestMiddleware_FailedAttemptReleasesForRetry(t *testing.T) {
	store := NewInMemoryStore()
	r := newRunner(store)

	r.fail = true
	failed := r.send(t, withKey("k1"))
	if failed.Result.ResultStatus() != benzene.StatusServiceUnavailable {
		t.Fatalf("status = %q, want service-unavailable", failed.Result.ResultStatus())
	}

	// The claim must have been released, so a redelivery reprocesses rather than being ignored.
	r.fail = false
	retry := r.send(t, withKey("k1"))
	if retry.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("retry status = %q, want ok (the failed attempt must have released the claim)", retry.Result.ResultStatus())
	}
	if r.ran != 2 {
		t.Errorf("handler ran %d times, want 2 - the released key must be reprocessed", r.ran)
	}
}

// inProgressStore always reports the key as claimed-and-in-progress by someone else.
type inProgressStore struct{}

func (inProgressStore) Claim(context.Context, string) (ClaimResult, error) {
	return ClaimResult{Won: false, Existing: Record{Status: InProgress}}, nil
}
func (inProgressStore) Complete(context.Context, string) error { return nil }
func (inProgressStore) Release(context.Context, string) error  { return nil }

func TestMiddleware_InProgressDuplicateIsConflict(t *testing.T) {
	r := newRunner(inProgressStore{})
	ic := r.send(t, withKey("k1"))
	if ic.Result.ResultStatus() != benzene.StatusConflict {
		t.Errorf("status = %q, want conflict for an in-progress duplicate", ic.Result.ResultStatus())
	}
	if r.ran != 0 {
		t.Errorf("handler ran %d times, want 0", r.ran)
	}
}

// erroringStore fails every Claim, to prove the middleware fails open.
type erroringStore struct{}

func (erroringStore) Claim(context.Context, string) (ClaimResult, error) {
	return ClaimResult{}, errors.New("store unavailable")
}
func (erroringStore) Complete(context.Context, string) error { return nil }
func (erroringStore) Release(context.Context, string) error  { return nil }

func TestMiddleware_StoreErrorFailsOpen(t *testing.T) {
	r := newRunner(erroringStore{})
	ic := r.send(t, withKey("k1"))
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok - a store outage must fail open (process)", ic.Result.ResultStatus())
	}
	if r.ran != 1 {
		t.Errorf("handler ran %d times, want 1", r.ran)
	}
}

// erroringNextStore lets the claim win but the downstream returns a Go error, to prove the claim is
// released on a pipeline-level error.
func TestMiddleware_PipelineErrorReleasesAndPropagates(t *testing.T) {
	store := NewInMemoryStore()
	boom := errors.New("boom")
	pipeline := benzene.NewPipeline(
		Middleware(store, HeaderKey(DefaultKeyHeader)),
		func(context.Context, *benzene.InvocationContext, func(context.Context) error) error { return boom },
	)
	ic := benzene.NewInvocationContext(benzene.NewTopic("t"), withKey("k1"), json.RawMessage(`{}`), benzene.NewContainer().NewScope())
	if err := pipeline.Run(context.Background(), ic); !errors.Is(err, boom) {
		t.Fatalf("pipeline error = %v, want the propagated boom", err)
	}
	// The claim must be released, so the key is re-claimable.
	claim, _ := store.Claim(context.Background(), benzene.NewTopic("t").String()+"\x00k1")
	if !claim.Won {
		t.Error("claim was not released after a pipeline error")
	}
}

func TestMiddleware_WinningAttemptWithNoResultReleases(t *testing.T) {
	// A winning attempt whose downstream produces no result at all is treated as unsuccessful, so
	// the claim is released rather than recorded as completed.
	store := NewInMemoryStore()
	pipeline := benzene.NewPipeline(
		Middleware(store, HeaderKey(DefaultKeyHeader)),
		func(context.Context, *benzene.InvocationContext, func(context.Context) error) error { return nil },
	)
	ic := benzene.NewInvocationContext(benzene.NewTopic("t"), withKey("k1"), json.RawMessage(`{}`), benzene.NewContainer().NewScope())
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if claim, _ := store.Claim(context.Background(), benzene.NewTopic("t").String()+"\x00k1"); !claim.Won {
		t.Error("a winning attempt that produced no result should release the claim")
	}
}

// foreignResult implements ResultInfo but NOT the optional ResultIsSuccessful interface, exercising
// resultSuccessful's status-class fallback.
type foreignResult struct{ status benzene.Status }

func (f foreignResult) ResultStatus() benzene.Status { return f.status }
func (f foreignResult) ResultErrors() []string       { return nil }
func (f foreignResult) ResultPayload() any           { return nil }

func TestMiddleware_ForeignResultUsesStatusClass(t *testing.T) {
	store := NewInMemoryStore()
	pipeline := benzene.NewPipeline(
		Middleware(store, HeaderKey(DefaultKeyHeader)),
		func(_ context.Context, ic *benzene.InvocationContext, _ func(context.Context) error) error {
			ic.Result = foreignResult{status: benzene.StatusOk}
			return nil
		},
	)
	ic := benzene.NewInvocationContext(benzene.NewTopic("t"), withKey("k1"), json.RawMessage(`{}`), benzene.NewContainer().NewScope())
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The foreign ok result is successful via the status-class fallback, so it is recorded completed.
	rec, _ := store.Claim(context.Background(), benzene.NewTopic("t").String()+"\x00k1")
	if rec.Won || rec.Existing.Status != Completed {
		t.Errorf("foreign ok result should record completed via the fallback; got %+v", rec)
	}
}

func TestHeaderKey(t *testing.T) {
	key := HeaderKey("Idempotency-Key")
	ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"), map[string]string{"idempotency-key": "abc"}, nil, nil)
	got := key(ic)
	if got == "" {
		t.Fatal("HeaderKey returned empty for a present header (should be case-insensitive)")
	}
	// Topic-scoped: the same value on a different topic yields a different key.
	other := benzene.NewInvocationContext(benzene.NewTopic("other"), map[string]string{"idempotency-key": "abc"}, nil, nil)
	if key(other) == got {
		t.Error("HeaderKey is not topic-scoped: same value on different topics collided")
	}
	// Absent header opts out.
	none := benzene.NewInvocationContext(benzene.NewTopic("order:create"), map[string]string{"other": "x"}, nil, nil)
	if key(none) != "" {
		t.Error("HeaderKey should return empty when the header is absent")
	}
}
