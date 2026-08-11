package cache_test

import (
	"context"
	"fmt"
	"time"

	"github.com/daniellepelley/benzene-go/cache"
)

// ExampleGetOrLoad shows the read-through cache helper: the first call misses and runs load (caching
// the value for the TTL); a second call within the TTL returns the cached value without calling load
// again. GetOrLoad degrades safely - a store read error is treated as a miss, a load error is returned
// and not cached.
func ExampleGetOrLoad() {
	store := cache.NewInMemoryStore()
	loads := 0
	load := func(context.Context) (int, error) {
		loads++
		return 42, nil
	}

	first, _ := cache.GetOrLoad(context.Background(), store, "answer", time.Minute, load)
	second, _ := cache.GetOrLoad(context.Background(), store, "answer", time.Minute, load)

	fmt.Printf("values: %d, %d; load called %d time(s)\n", first, second, loads)
	// Output: values: 42, 42; load called 1 time(s)
}
