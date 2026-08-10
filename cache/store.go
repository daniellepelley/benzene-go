package cache

import (
	"context"
	"sync"
	"time"
)

// InMemoryStore is an in-process Store backed by a map, for a single instance, tests, and local
// development. State lives in this process only: in a multi-instance deployment each instance keeps
// its own map (so a fleet does not share a cache - use a shared store there). It is safe for
// concurrent use.
//
// Entries are expired LAZILY, on the next Get of that key - there is no background sweeper, so an
// entry that is written once and never read again is not reclaimed. This is fine for a bounded key
// space; for an unbounded or ever-growing key space it is unbounded memory growth, so use a shared
// store (or a bounded set of keys) there.
type InMemoryStore struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	now     func() time.Time
}

type cacheEntry struct {
	value     string
	expiresAt time.Time // zero means no expiry
}

// InMemoryOption configures an InMemoryStore.
type InMemoryOption func(*InMemoryStore)

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) InMemoryOption {
	return func(s *InMemoryStore) { s.now = now }
}

// NewInMemoryStore returns an empty InMemoryStore using the wall clock, overridable via options.
func NewInMemoryStore(opts ...InMemoryOption) *InMemoryStore {
	s := &InMemoryStore{entries: make(map[string]cacheEntry), now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Get returns the value for key unless it is absent or expired.
func (s *InMemoryStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return "", false, nil
	}
	if !entry.expiresAt.IsZero() && !entry.expiresAt.After(s.now()) {
		delete(s.entries, key)
		return "", false, nil
	}
	return entry.value, true, nil
}

// Set stores value for key for ttl; a ttl <= 0 stores it with no expiry.
func (s *InMemoryStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := cacheEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = s.now().Add(ttl)
	}
	s.entries[key] = entry
	return nil
}

// Delete removes key.
func (s *InMemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.entries, key)
	return nil
}
