package cache

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestInMemoryStore_SetGetDelete(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()

	if _, found, _ := s.Get(ctx, "k"); found {
		t.Error("absent key reported as found")
	}

	s.Set(ctx, "k", "v", time.Minute)
	if v, found, err := s.Get(ctx, "k"); err != nil || !found || v != "v" {
		t.Errorf("Get after Set = %q,%v,%v; want v,true,nil", v, found, err)
	}

	s.Delete(ctx, "k")
	if _, found, _ := s.Get(ctx, "k"); found {
		t.Error("key still present after Delete")
	}
}

func TestInMemoryStore_Expiry(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewInMemoryStore(WithClock(func() time.Time { return now }))
	ctx := context.Background()

	s.Set(ctx, "k", "v", time.Minute)
	now = now.Add(30 * time.Second)
	if _, found, _ := s.Get(ctx, "k"); !found {
		t.Error("entry expired before its TTL")
	}
	now = now.Add(time.Minute)
	if _, found, _ := s.Get(ctx, "k"); found {
		t.Error("entry did not expire after its TTL")
	}
}

func TestInMemoryStore_NonPositiveTTLNeverExpires(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewInMemoryStore(WithClock(func() time.Time { return now }))
	ctx := context.Background()

	s.Set(ctx, "k", "v", 0)
	now = now.Add(1000 * time.Hour)
	if _, found, _ := s.Get(ctx, "k"); !found {
		t.Error("a zero-TTL entry expired, want no-expiry")
	}
}

func TestInMemoryStore_ConcurrentAccessIsRaceFree(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Set(ctx, "k", "v", time.Minute)
			s.Get(ctx, "k")
			s.Delete(ctx, "k")
		}()
	}
	wg.Wait()
}
