package idempotency

import (
	"context"
	"sync"
	"time"
)

// Default retention windows for InMemoryStore (see the type doc for why they differ).
const (
	defaultLeaseTTL     = 5 * time.Minute
	defaultCompletedTTL = 24 * time.Hour
)

// InMemoryStore is an in-process Store backed by a map, for a single worker instance, tests, and
// local development. State lives in this process only: in a multi-instance deployment each instance
// keeps its own map, so a duplicate redelivered to a different instance is NOT de-duplicated - use a
// shared store there. It is safe for concurrent use.
//
// It keeps two distinct retention windows, which a persistent Store SHOULD mirror:
//   - the in-progress LEASE TTL (default 5m) bounds how long a claimed-but-unsettled key blocks
//     duplicates. It must outlive a handler's normal run time but stay well under the transport's
//     redelivery-exhaustion window, so that a worker which claims a key and then crashes (before
//     Complete/Release) frees the key for a redelivery to reprocess rather than nacking every
//     redelivery to the dead-letter queue for the whole dedup window.
//   - the completed DEDUP TTL (default 24h) is how long a finished key suppresses duplicates; keep
//     it longer than the transport's maximum redelivery window.
//
// A record is expired lazily on the next access to its key.
type InMemoryStore struct {
	mu           sync.Mutex
	entries      map[string]entry
	leaseTTL     time.Duration
	completedTTL time.Duration
	now          func() time.Time
}

type entry struct {
	status    Status
	expiresAt time.Time
}

// InMemoryOption configures an InMemoryStore.
type InMemoryOption func(*InMemoryStore)

// WithLeaseTTL sets how long an in-progress lease is held before a claimed-but-unsettled key becomes
// re-claimable (default 5m). Set it longer than the slowest handler but shorter than the transport's
// redelivery-exhaustion window.
func WithLeaseTTL(ttl time.Duration) InMemoryOption {
	return func(s *InMemoryStore) { s.leaseTTL = ttl }
}

// WithCompletedTTL sets how long a completed key de-duplicates before it expires (default 24h). Keep
// it longer than the transport's maximum redelivery window.
func WithCompletedTTL(ttl time.Duration) InMemoryOption {
	return func(s *InMemoryStore) { s.completedTTL = ttl }
}

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) InMemoryOption {
	return func(s *InMemoryStore) { s.now = now }
}

// NewInMemoryStore returns an InMemoryStore with the default lease/completed TTLs and the wall
// clock, overridable via options.
func NewInMemoryStore(opts ...InMemoryOption) *InMemoryStore {
	s := &InMemoryStore{
		entries:      make(map[string]entry),
		leaseTTL:     defaultLeaseTTL,
		completedTTL: defaultCompletedTTL,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Claim atomically claims key, treating an expired entry (a lapsed in-progress lease or a lapsed
// completed record) as absent and therefore re-claimable.
func (s *InMemoryStore) Claim(_ context.Context, key string) (ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if e, ok := s.entries[key]; ok && e.expiresAt.After(now) {
		return ClaimResult{Won: false, Existing: Record{Status: e.status}}, nil
	}
	s.entries[key] = entry{status: InProgress, expiresAt: now.Add(s.leaseTTL)}
	return ClaimResult{Won: true}, nil
}

// Complete promotes key to Completed and extends its retention to the completed-dedup window. A key
// that is no longer present (expired and evicted) is a no-op.
func (s *InMemoryStore) Complete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[key]; ok {
		e.status = Completed
		e.expiresAt = s.now().Add(s.completedTTL)
		s.entries[key] = e
	}
	return nil
}

// Release removes key so a redelivery can reprocess it.
func (s *InMemoryStore) Release(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.entries, key)
	return nil
}
