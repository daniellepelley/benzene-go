package idempotency

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestInMemoryStore_ClaimWinsThenLoses(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()

	first, _ := s.Claim(ctx, "k")
	if !first.Won {
		t.Fatal("first Claim did not win")
	}
	second, _ := s.Claim(ctx, "k")
	if second.Won {
		t.Error("second Claim won - a live claim must block a re-claim")
	}
	if second.Existing.Status != InProgress {
		t.Errorf("existing status = %v, want InProgress", second.Existing.Status)
	}
}

func TestInMemoryStore_CompleteRecordsOutcome(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	s.Claim(ctx, "k")
	if err := s.Complete(ctx, "k", true); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	got, _ := s.Claim(ctx, "k")
	if got.Won || got.Existing.Status != Completed || !got.Existing.Successful {
		t.Errorf("after Complete: claim = %+v, want a completed, successful record", got)
	}
}

func TestInMemoryStore_ReleaseAllowsReclaim(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	s.Claim(ctx, "k")
	if err := s.Release(ctx, "k"); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	again, _ := s.Claim(ctx, "k")
	if !again.Won {
		t.Error("Claim after Release did not win - a released key must be re-claimable")
	}
}

func TestInMemoryStore_ExpiredEntryIsReclaimable(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewInMemoryStore(WithTTL(time.Minute), WithClock(func() time.Time { return now }))
	ctx := context.Background()

	s.Claim(ctx, "k") // claimed at t=0, expires at t=1m
	now = now.Add(2 * time.Minute)

	got, _ := s.Claim(ctx, "k")
	if !got.Won {
		t.Error("an expired claim was not re-claimable")
	}
}

func TestInMemoryStore_CompleteAndReleaseOnMissingKeyAreNoops(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	if err := s.Complete(ctx, "absent", true); err != nil {
		t.Errorf("Complete(absent) error = %v, want nil no-op", err)
	}
	if err := s.Release(ctx, "absent"); err != nil {
		t.Errorf("Release(absent) error = %v, want nil no-op", err)
	}
}

func TestInMemoryStore_ConcurrentClaimsElectOneWinner(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()

	const n = 50
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if c, _ := s.Claim(ctx, "k"); c.Won {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("%d concurrent claimers won, want exactly 1", wins)
	}
}
