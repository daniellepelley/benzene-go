// Package cache is the caching building block, matching the essence of Benzene.Cache.Core: a
// pluggable Store plus a read-through (cache-aside) helper a handler calls around an expensive read.
// It is zero-dependency - the InMemoryStore default is a standard-library map with per-entry TTLs,
// and a shared store (Redis, Memcached) implements the same Store interface in its own module.
//
// The primary API is GetOrLoad: it returns the cached value for a key, or calls the loader once,
// caches its result, and returns it - the Go form of the .NET CacheEntry.LazyLoad. Caching is a
// handler-level concern (which reads are cacheable, and under what key, is application knowledge),
// so this is a helper a handler calls, not a pipeline middleware.
//
// Degradation is the cache rule: a store read error is treated as a miss and a store write error is
// ignored, so a cache outage falls back to loading every time (reduced performance) rather than
// failing the request; and a loader error is returned and NOT cached, so a transient failure is not
// remembered.
package cache

import (
	"context"
	"encoding/json"
	"time"
)

// Store is pluggable cache persistence: string values keyed by string, each retained for a TTL. Swap
// the implementation to change where entries live (InMemoryStore for one instance/tests; a shared
// store for a fleet) without touching call sites. Implementations MUST be safe for concurrent use.
type Store interface {
	// Get returns the value stored for key and whether it was present (and unexpired). A non-nil
	// error means the store could not be read; callers treat that as a miss.
	Get(ctx context.Context, key string) (value string, found bool, err error)
	// Set stores value for key for at most ttl. A ttl <= 0 stores it with no expiry.
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// Delete removes key (a no-op if absent), e.g. to invalidate a cached value after a write.
	Delete(ctx context.Context, key string) error
}

// GetOrLoad returns the cached value for key, or calls load once, caches its result for ttl, and
// returns it (cache-aside / read-through). Values are JSON-serialized in the store, so T must be
// JSON-round-trippable.
//
// Misses and failures degrade safely: a store read error or an unparseable cached value falls
// through to load (the store is not trusted to break the call); load's error is returned and the
// value is NOT cached (a transient failure is not remembered); and a store write error is ignored
// (the freshly-loaded value is still returned). A cache hit never calls load.
func GetOrLoad[T any](ctx context.Context, store Store, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	if raw, found, err := store.Get(ctx, key); err == nil && found {
		var cached T
		if json.Unmarshal([]byte(raw), &cached) == nil {
			return cached, nil
		}
	}

	value, err := load(ctx)
	if err != nil {
		return value, err
	}

	if raw, err := json.Marshal(value); err == nil {
		_ = store.Set(ctx, key, string(raw), ttl)
	}
	return value, nil
}
