package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

type order struct {
	ID     string
	Amount float64
}

func TestGetOrLoad_MissLoadsAndCaches(t *testing.T) {
	store := NewInMemoryStore()
	loads := 0
	load := func(context.Context) (order, error) {
		loads++
		return order{ID: "o-1", Amount: 9.99}, nil
	}

	// First call misses -> loads and caches.
	got, err := GetOrLoad(context.Background(), store, "order:o-1", time.Minute, load)
	if err != nil || got.ID != "o-1" || got.Amount != 9.99 {
		t.Fatalf("first GetOrLoad = %+v, %v; want the loaded order", got, err)
	}
	// Second call hits -> does not load again.
	got, err = GetOrLoad(context.Background(), store, "order:o-1", time.Minute, load)
	if err != nil || got.ID != "o-1" {
		t.Fatalf("second GetOrLoad = %+v, %v", got, err)
	}
	if loads != 1 {
		t.Errorf("loader ran %d times, want 1 - the second call must be a cache hit", loads)
	}
}

func TestGetOrLoad_LoaderErrorIsReturnedAndNotCached(t *testing.T) {
	store := NewInMemoryStore()
	boom := errors.New("db down")
	loads := 0
	load := func(context.Context) (order, error) {
		loads++
		return order{}, boom
	}

	if _, err := GetOrLoad(context.Background(), store, "k", time.Minute, load); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the loader error", err)
	}
	// The failure must not be cached: a second call retries the loader.
	GetOrLoad(context.Background(), store, "k", time.Minute, load)
	if loads != 2 {
		t.Errorf("loader ran %d times, want 2 - a load error must not be cached", loads)
	}
}

// failingReadStore errors on Get, to prove GetOrLoad falls open to loading.
type failingReadStore struct{ Store }

func (failingReadStore) Get(context.Context, string) (string, bool, error) {
	return "", false, errors.New("store unavailable")
}

func TestGetOrLoad_StoreReadErrorFallsOpen(t *testing.T) {
	store := failingReadStore{Store: NewInMemoryStore()}
	loaded := false
	got, err := GetOrLoad(context.Background(), store, "k", time.Minute, func(context.Context) (order, error) {
		loaded = true
		return order{ID: "o-9"}, nil
	})
	if err != nil || got.ID != "o-9" || !loaded {
		t.Errorf("a store read error should fall open to load; got %+v, %v, loaded=%v", got, err, loaded)
	}
}

func TestGetOrLoad_UnparseableCachedValueReloads(t *testing.T) {
	store := NewInMemoryStore()
	// Poison the key with a value that is not valid JSON for T.
	store.Set(context.Background(), "k", "{not json", time.Minute)
	got, err := GetOrLoad(context.Background(), store, "k", time.Minute, func(context.Context) (order, error) {
		return order{ID: "fresh"}, nil
	})
	if err != nil || got.ID != "fresh" {
		t.Errorf("an unparseable cached value should reload; got %+v, %v", got, err)
	}
}

// failingWriteStore reads as a miss and errors on Set, to prove a write error is ignored.
type failingWriteStore struct{ Store }

func (failingWriteStore) Get(context.Context, string) (string, bool, error) { return "", false, nil }
func (failingWriteStore) Set(context.Context, string, string, time.Duration) error {
	return errors.New("write failed")
}

func TestGetOrLoad_StoreWriteErrorIsIgnored(t *testing.T) {
	store := failingWriteStore{Store: NewInMemoryStore()}
	got, err := GetOrLoad(context.Background(), store, "k", time.Minute, func(context.Context) (order, error) {
		return order{ID: "o-1"}, nil
	})
	if err != nil || got.ID != "o-1" {
		t.Errorf("a store write error should be ignored and the value still returned; got %+v, %v", got, err)
	}
}
