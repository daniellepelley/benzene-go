package benzene

import (
	"sync"
	"sync/atomic"
	"testing"
)

type counter struct{ n int }

func TestSingleton_SameInstanceAcrossScopes(t *testing.T) {
	c := NewContainer()
	calls := 0
	AddSingleton(c, "counter", func(s *Scope) *counter {
		calls++
		return &counter{n: calls}
	})

	first := GetService[*counter](c.NewScope(), "counter")
	second := GetService[*counter](c.NewScope(), "counter")

	if first != second {
		t.Error("singleton should return the same instance across different scopes")
	}
	if calls != 1 {
		t.Errorf("factory called %d times, want exactly 1", calls)
	}
}

func TestScoped_SameInstanceWithinScope_DifferentAcrossScopes(t *testing.T) {
	c := NewContainer()
	calls := 0
	AddScoped(c, "counter", func(s *Scope) *counter {
		calls++
		return &counter{n: calls}
	})

	scopeA := c.NewScope()
	a1 := GetService[*counter](scopeA, "counter")
	a2 := GetService[*counter](scopeA, "counter")
	if a1 != a2 {
		t.Error("scoped service should return the same instance within one scope")
	}

	scopeB := c.NewScope()
	b1 := GetService[*counter](scopeB, "counter")
	if a1 == b1 {
		t.Error("scoped service should return a different instance in a different scope")
	}
	if calls != 2 {
		t.Errorf("factory called %d times, want exactly 2 (once per scope)", calls)
	}
}

func TestTransient_NewInstanceEveryResolve(t *testing.T) {
	c := NewContainer()
	calls := 0
	AddTransient(c, "counter", func(s *Scope) *counter {
		calls++
		return &counter{n: calls}
	})

	scope := c.NewScope()
	first := GetService[*counter](scope, "counter")
	second := GetService[*counter](scope, "counter")

	if first == second {
		t.Error("transient service should return a new instance on every resolve")
	}
	if calls != 2 {
		t.Errorf("factory called %d times, want exactly 2", calls)
	}
}

func TestTryAdd_DoesNotOverrideExistingRegistration(t *testing.T) {
	c := NewContainer()
	AddSingleton(c, "counter", func(s *Scope) *counter { return &counter{n: 1} })
	TryAddSingleton(c, "counter", func(s *Scope) *counter { return &counter{n: 2} })

	got := GetService[*counter](c.NewScope(), "counter")
	if got.n != 1 {
		t.Errorf("counter.n = %d, want 1 (the app's own Add* registration should win over a later TryAdd*)", got.n)
	}
}

func TestTryAdd_RegistersWhenAbsent(t *testing.T) {
	c := NewContainer()
	TryAddScoped(c, "counter", func(s *Scope) *counter { return &counter{n: 1} })

	got, ok := TryGetService[*counter](c.NewScope(), "counter")
	if !ok || got.n != 1 {
		t.Errorf("TryGetService() = (%v, %v), want (&counter{n:1}, true)", got, ok)
	}
}

func TestTryAddTransient_RegistersWhenAbsent(t *testing.T) {
	c := NewContainer()
	TryAddTransient(c, "counter", func(s *Scope) *counter { return &counter{n: 7} })

	got := GetService[*counter](c.NewScope(), "counter")
	if got.n != 7 {
		t.Errorf("counter.n = %d, want 7", got.n)
	}
}

func TestTryGetService_MissingRegistrationReturnsFalse(t *testing.T) {
	c := NewContainer()
	_, ok := TryGetService[*counter](c.NewScope(), "missing")
	if ok {
		t.Error("TryGetService() for an unregistered key should return ok = false")
	}
}

func TestGetService_MissingRegistrationPanics(t *testing.T) {
	c := NewContainer()
	defer func() {
		if recover() == nil {
			t.Error("GetService() for an unregistered key should panic")
		}
	}()
	GetService[*counter](c.NewScope(), "missing")
}

// TestSingleton_ConcurrentResolutionKeepsOneInstance exercises typedSingleton's
// double-checked-locking race branch deterministically: startedWg guarantees every
// goroutine has passed the initial "not yet created" check and is blocked inside the
// factory before the test releases them all at once via ready, so multiple factory calls
// (and the "someone else already stored it" discard branch) are guaranteed, not just likely.
func TestSingleton_ConcurrentResolutionKeepsOneInstance(t *testing.T) {
	c := NewContainer()
	var calls int32
	const goroutines = 8
	ready := make(chan struct{})
	var startedWg sync.WaitGroup
	startedWg.Add(goroutines)
	AddSingleton(c, "counter", func(s *Scope) *counter {
		startedWg.Done()
		<-ready
		n := atomic.AddInt32(&calls, 1)
		return &counter{n: int(n)}
	})

	results := make([]*counter, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = GetService[*counter](c.NewScope(), "counter")
		}(i)
	}
	startedWg.Wait() // every goroutine is now blocked inside the factory, past the first check
	close(ready)     // release them all at once so multiple factory calls race to store
	wg.Wait()

	if calls < 2 {
		t.Fatalf("factory called %d times, want at least 2 (the race this test targets didn't happen)", calls)
	}
	first := results[0]
	for i, r := range results {
		if r != first {
			t.Errorf("results[%d] = %p, want the same instance as results[0] = %p", i, r, first)
		}
	}
}

// TestScoped_ConcurrentResolutionKeepsOneInstance is typedScoped's counterpart: multiple
// goroutines resolving the same key on the SAME scope concurrently.
func TestScoped_ConcurrentResolutionKeepsOneInstance(t *testing.T) {
	c := NewContainer()
	var calls int32
	const goroutines = 8
	ready := make(chan struct{})
	var startedWg sync.WaitGroup
	startedWg.Add(goroutines)
	AddScoped(c, "counter", func(s *Scope) *counter {
		startedWg.Done()
		<-ready
		n := atomic.AddInt32(&calls, 1)
		return &counter{n: int(n)}
	})

	scope := c.NewScope()
	results := make([]*counter, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = GetService[*counter](scope, "counter")
		}(i)
	}
	startedWg.Wait()
	close(ready)
	wg.Wait()

	if calls < 2 {
		t.Fatalf("factory called %d times, want at least 2 (the race this test targets didn't happen)", calls)
	}
	first := results[0]
	for i, r := range results {
		if r != first {
			t.Errorf("results[%d] = %p, want the same instance as results[0] = %p", i, r, first)
		}
	}
}

func TestScope_ScopedFactoryCanResolveOtherServicesFromTheSameScope(t *testing.T) {
	c := NewContainer()
	AddScoped(c, "counter", func(s *Scope) *counter { return &counter{n: 1} })
	AddScoped(c, "doubled", func(s *Scope) *counter {
		inner := GetService[*counter](s, "counter")
		return &counter{n: inner.n * 2}
	})

	got := GetService[*counter](c.NewScope(), "doubled")
	if got.n != 2 {
		t.Errorf("doubled.n = %d, want 2", got.n)
	}
}
