package ratelimiting

import (
	"sync"
	"time"
)

// TokenBucket is a standard-library, thread-safe token-bucket Limiter: it refills at a fixed rate up
// to a burst capacity, and a message is allowed when the bucket holds at least its cost in tokens.
// It is the zero-dependency default; the release it returns is always a no-op (spent tokens are not
// returned - a token bucket limits rate, not concurrency).
type TokenBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens added per second
	burst  float64 // maximum tokens the bucket holds
	tokens float64
	last   time.Time
	now    func() time.Time
}

// TokenBucketOption configures a TokenBucket.
type TokenBucketOption func(*TokenBucket)

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) TokenBucketOption {
	return func(b *TokenBucket) { b.now = now }
}

// NewTokenBucket returns a token bucket that refills at ratePerSecond tokens per second up to burst
// tokens, starting full. A burst below 1 is raised to 1 so a cost-1 message can ever be admitted. A
// negative rate is clamped to 0 (a fixed quota of burst, no refill): a negative refill would drive
// the token count unbounded-negative and wedge the bucket into permanent rejection, even for the
// always-free zero-cost path. A rate of exactly 0 is a valid fixed quota.
func NewTokenBucket(ratePerSecond float64, burst int, opts ...TokenBucketOption) *TokenBucket {
	capacity := float64(burst)
	if capacity < 1 {
		capacity = 1
	}
	if ratePerSecond < 0 {
		ratePerSecond = 0
	}
	b := &TokenBucket{rate: ratePerSecond, burst: capacity, tokens: capacity, now: time.Now}
	for _, opt := range opts {
		opt(b)
	}
	b.last = b.now()
	return b
}

// Allow refills the bucket for the elapsed time and takes cost tokens if available. A cost above the
// burst capacity can never be granted, so it is always rejected (matching the "payload larger than
// the whole bucket" case). The returned release is a no-op.
func (b *TokenBucket) Allow(cost int) (func(), bool) {
	noop := func() {}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}

	if float64(cost) <= b.tokens {
		b.tokens -= float64(cost)
		return noop, true
	}
	return noop, false
}
