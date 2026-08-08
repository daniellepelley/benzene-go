package idempotency

import (
	"context"
	"sync"
	"time"
)

// InMemoryStore is an in-process Store backed by a map, for a single worker instance, tests, and
// local development. State lives in this process only: in a multi-instance deployment each instance
// keeps its own map, so a duplicate redelivered to a different instance is NOT de-duplicated - use a
// shared store there. Records are held for a time-to-live and expired lazily on the next access to a
// key. It is safe for concurrent use.
type InMemoryStore struct {
	mu      sync.Mutex
	entries map[string]entry
	ttl     time.Duration
	now     func() time.Time
}

type entry struct {
	status     Status
	successful bool
	expiresAt  time.Time
}

// InMemoryOption configures an InMemoryStore.
type InMemoryOption func(*InMemoryStore)

// WithTTL sets how long a record is retained before it expires (default 24h). Keep it longer than
// the transport's maximum redelivery window so a late redelivery is still de-duplicated.
func WithTTL(ttl time.Duration) InMemoryOption {
	return func(s *InMemoryStore) { s.ttl = ttl }
}

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) InMemoryOption {
	return func(s *InMemoryStore) { s.now = now }
}

// NewInMemoryStore returns an InMemoryStore with a 24-hour TTL and the wall clock, overridable via
// options.
func NewInMemoryStore(opts ...InMemoryOption) *InMemoryStore {
	s := &InMemoryStore{entries: make(map[string]entry), ttl: 24 * time.Hour, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Claim atomically claims key, treating an expired entry as absent.
func (s *InMemoryStore) Claim(_ context.Context, key string) (ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if e, ok := s.entries[key]; ok && e.expiresAt.After(now) {
		return ClaimResult{Won: false, Existing: Record{Status: e.status, Successful: e.successful}}, nil
	}
	s.entries[key] = entry{status: InProgress, expiresAt: now.Add(s.ttl)}
	return ClaimResult{Won: true}, nil
}

// Complete promotes key to Completed and refreshes its TTL. A key that is no longer present (expired
// and evicted) is a no-op.
func (s *InMemoryStore) Complete(_ context.Context, key string, successful bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[key]; ok {
		e.status = Completed
		e.successful = successful
		e.expiresAt = s.now().Add(s.ttl)
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
