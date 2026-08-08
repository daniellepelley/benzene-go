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
	if err := s.Complete(ctx, "k"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	got, _ := s.Claim(ctx, "k")
	if got.Won || got.Existing.Status != Completed {
		t.Errorf("after Complete: claim = %+v, want a completed record", got)
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
	s := NewInMemoryStore(WithLeaseTTL(time.Minute), WithClock(func() time.Time { return now }))
	ctx := context.Background()

	s.Claim(ctx, "k") // claimed at t=0, expires at t=1m
	now = now.Add(2 * time.Minute)

	got, _ := s.Claim(ctx, "k")
	if !got.Won {
		t.Error("an expired claim was not re-claimable")
	}
}

func TestInMemoryStore_LapsedLeaseIsReclaimable(t *testing.T) {
	// A short in-progress lease must free the key even if the first worker never settles it (crashed
	// after Claim), so a redelivery can reprocess rather than being nacked for the whole dedup window.
	now := time.Unix(0, 0)
	s := NewInMemoryStore(WithLeaseTTL(time.Minute), WithCompletedTTL(time.Hour), WithClock(func() time.Time { return now }))

	s.Claim(context.Background(), "stuck") // in-progress, lease 1m
	now = now.Add(2 * time.Minute)
	if c, _ := s.Claim(context.Background(), "stuck"); !c.Won {
		t.Error("a lapsed in-progress lease should be re-claimable so a crashed worker never stalls the key")
	}
}

func TestInMemoryStore_CompletedSurvivesLeaseTTLThenExpires(t *testing.T) {
	// A completed key must keep de-duplicating for the full (longer) completed window, not just the
	// short lease window - proving the two TTLs are independent.
	now := time.Unix(0, 0)
	s := NewInMemoryStore(WithLeaseTTL(time.Minute), WithCompletedTTL(time.Hour), WithClock(func() time.Time { return now }))
	ctx := context.Background()
	s.Claim(ctx, "done")
	s.Complete(ctx, "done")

	now = now.Add(2 * time.Minute) // past the lease TTL, still within the completed TTL
	if c, _ := s.Claim(ctx, "done"); c.Won || c.Existing.Status != Completed {
		t.Error("a completed key must keep de-duplicating past the lease TTL")
	}
	now = now.Add(time.Hour) // past the completed TTL
	if c, _ := s.Claim(ctx, "done"); !c.Won {
		t.Error("a completed key must expire after the completed TTL")
	}
}

func TestInMemoryStore_CompleteAndReleaseOnMissingKeyAreNoops(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	if err := s.Complete(ctx, "absent"); err != nil {
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
