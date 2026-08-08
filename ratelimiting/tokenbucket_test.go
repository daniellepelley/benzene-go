package ratelimiting

import (
	"sync"
	"testing"
	"time"
)

func TestTokenBucket_StartsFullThenRejects(t *testing.T) {
	frozen := time.Unix(0, 0)
	b := NewTokenBucket(1, 3, WithClock(func() time.Time { return frozen }))

	// Burst of 3, no time passing: three permits granted, the fourth rejected.
	for i := 0; i < 3; i++ {
		if _, ok := b.Allow(1); !ok {
			t.Fatalf("permit %d rejected, want granted from the initial burst", i+1)
		}
	}
	if _, ok := b.Allow(1); ok {
		t.Error("fourth permit granted, want rejected (burst exhausted, no refill)")
	}
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewTokenBucket(2, 2, WithClock(func() time.Time { return now })) // 2 tokens/sec, burst 2

	b.Allow(2) // drain the bucket
	if _, ok := b.Allow(1); ok {
		t.Fatal("granted while empty")
	}

	now = now.Add(time.Second) // +1s => +2 tokens (capped at burst 2)
	if _, ok := b.Allow(1); !ok {
		t.Error("not granted after a refill")
	}
	if _, ok := b.Allow(1); !ok {
		t.Error("second refilled token not granted")
	}
	if _, ok := b.Allow(1); ok {
		t.Error("granted a third token, but only 2 refilled")
	}
}

func TestTokenBucket_RefillCapsAtBurst(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewTokenBucket(1, 2, WithClock(func() time.Time { return now }))
	b.Allow(2) // empty

	now = now.Add(time.Hour) // huge elapsed time, but refill caps at burst 2
	if _, ok := b.Allow(2); !ok {
		t.Fatal("full burst not available after a long refill")
	}
	if _, ok := b.Allow(1); ok {
		t.Error("more than burst accumulated - refill did not cap at capacity")
	}
}

func TestTokenBucket_CostAboveBurstAlwaysRejected(t *testing.T) {
	b := NewTokenBucket(1000, 5)
	if _, ok := b.Allow(6); ok {
		t.Error("a cost above the burst capacity was granted, want always rejected")
	}
}

func TestTokenBucket_ZeroCostAlwaysAllowed(t *testing.T) {
	frozen := time.Unix(0, 0)
	b := NewTokenBucket(0, 1, WithClock(func() time.Time { return frozen }))
	b.Allow(1) // drain
	if _, ok := b.Allow(0); !ok {
		t.Error("a zero-cost message was rejected, want always allowed")
	}
}

func TestNewTokenBucket_BurstBelowOneRaisedToOne(t *testing.T) {
	b := NewTokenBucket(1, 0)
	if _, ok := b.Allow(1); !ok {
		t.Error("burst 0 was not raised to 1 - a cost-1 message can never be admitted")
	}
}

func TestNewTokenBucket_NegativeRateClampedToFixedQuota(t *testing.T) {
	// A negative rate must be clamped to 0 (a fixed quota of burst), not drive tokens negative and
	// wedge the bucket into permanent rejection.
	now := time.Unix(0, 0)
	b := NewTokenBucket(-5, 2, WithClock(func() time.Time { return now }))

	if _, ok := b.Allow(2); !ok {
		t.Fatal("the initial burst was not granted with a negative (clamped) rate")
	}
	now = now.Add(time.Hour) // time passes; a negative rate would have subtracted tokens
	if _, ok := b.Allow(0); !ok {
		t.Error("zero-cost rejected after a negative-rate config - the clamp did not hold the floor")
	}
	if _, ok := b.Allow(1); ok {
		t.Error("granted a permit with no refill - a clamped negative rate must not refill")
	}
}

func TestTokenBucket_ReleaseIsNoop(t *testing.T) {
	b := NewTokenBucket(1, 1)
	release, ok := b.Allow(1)
	if !ok {
		t.Fatal("not granted")
	}
	release() // must not panic and must not return the token
	if _, ok := b.Allow(1); ok {
		t.Error("release returned the spent token - a token bucket limits rate, not concurrency")
	}
}

func TestTokenBucket_ConcurrentAllowIsRaceFree(t *testing.T) {
	// With a burst of exactly N and no refill (frozen clock), exactly N of the concurrent callers
	// must be granted - proving the take is atomic.
	frozen := time.Unix(0, 0)
	const n = 100
	b := NewTokenBucket(0, n, WithClock(func() time.Time { return frozen }))

	var granted int
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(n * 2)
	for i := 0; i < n*2; i++ {
		go func() {
			defer wg.Done()
			if _, ok := b.Allow(1); ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if granted != n {
		t.Errorf("%d granted, want exactly %d (the bucket held %d tokens)", granted, n, n)
	}
}
