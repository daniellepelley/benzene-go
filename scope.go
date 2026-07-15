package benzene

import (
	"context"
	"fmt"
	"sync"
)

// Registry does not describe scope keys, so a small comparable key type identifies a
// registered service - typically a pointer to an interface's zero value, or a
// distinct string/type constant the application defines. Using `any` as the map key
// keeps this simple; callers should use a package-level unexported type or a stable
// string constant as their key to avoid collisions.
type serviceKey = any

type lifetime int

const (
	lifetimeSingleton lifetime = iota
	lifetimeScoped
	lifetimeTransient
)

type registration struct {
	lifetime lifetime
	factory  func(s *Scope) any
}

// Container is the shared registration set an application configures once at startup
// (core-concepts.md §8): singleton/scoped/transient registrations, by factory. Languages
// without a DI culture (Go included) MAY implement the container-abstraction concept as an
// explicit registry/context object rather than a full framework - this Container is that
// explicit object, not a general-purpose reflection-based DI container.
type Container struct {
	mu            sync.Mutex
	registrations map[serviceKey]registration
	singletons    map[serviceKey]any
}

// NewContainer returns an empty Container.
func NewContainer() *Container {
	return &Container{
		registrations: make(map[serviceKey]registration),
		singletons:    make(map[serviceKey]any),
	}
}

// AddSingleton registers factory to be called at most once; the same instance is reused
// for every scope thereafter.
func AddSingleton[T any](c *Container, key serviceKey, factory func(s *Scope) T) {
	c.add(key, lifetimeSingleton, wrapFactory(factory))
}

// AddScoped registers factory to be called once per invocation scope; the same instance is
// reused for the lifetime of that scope, then discarded.
func AddScoped[T any](c *Container, key serviceKey, factory func(s *Scope) T) {
	c.add(key, lifetimeScoped, wrapFactory(factory))
}

// AddTransient registers factory to be called every time the service is resolved.
func AddTransient[T any](c *Container, key serviceKey, factory func(s *Scope) T) {
	c.add(key, lifetimeTransient, wrapFactory(factory))
}

// TryAddSingleton registers factory as a singleton only if key has no registration yet -
// this is how framework defaults are made overridable (core-concepts.md §8): the framework
// tryAdds its defaults, and the application's own explicit Add* registration (applied
// first) wins.
func TryAddSingleton[T any](c *Container, key serviceKey, factory func(s *Scope) T) {
	c.tryAdd(key, lifetimeSingleton, wrapFactory(factory))
}

// TryAddScoped is TryAddSingleton's scoped-lifetime counterpart.
func TryAddScoped[T any](c *Container, key serviceKey, factory func(s *Scope) T) {
	c.tryAdd(key, lifetimeScoped, wrapFactory(factory))
}

// TryAddTransient is TryAddSingleton's transient-lifetime counterpart.
func TryAddTransient[T any](c *Container, key serviceKey, factory func(s *Scope) T) {
	c.tryAdd(key, lifetimeTransient, wrapFactory(factory))
}

func (c *Container) add(key serviceKey, lt lifetime, factory func(s *Scope) any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registrations[key] = registration{lifetime: lt, factory: factory}
}

func (c *Container) tryAdd(key serviceKey, lt lifetime, factory func(s *Scope) any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.registrations[key]; exists {
		return
	}
	c.registrations[key] = registration{lifetime: lt, factory: factory}
}

// wrapFactory type-erases a generic factory into the non-generic shape Container stores.
// T is known at this call site (inside a generic AddSingleton/AddScoped/... call), which is
// what makes this a simple closure rather than reflection.
func wrapFactory[T any](factory func(s *Scope) T) func(s *Scope) any {
	return func(s *Scope) any { return factory(s) }
}

// NewScope creates a new per-invocation scope over c. A scope is created per pipeline
// invocation; scoped services live and die with it (core-concepts.md §8).
func (c *Container) NewScope() *Scope {
	return &Scope{container: c, scoped: make(map[serviceKey]any)}
}

// Scope resolves services for a single invocation. GetService/TryGetService are the only
// resolution operations (core-concepts.md §8).
type Scope struct {
	container *Container
	mu        sync.Mutex
	scoped    map[serviceKey]any
}

// GetService resolves key, panicking if it has no registration - mirroring the spec's
// "required" resolution operation, which throws/panics rather than returning a zero value
// on a missing registration (a missing required dependency is a programming error, not a
// recoverable runtime condition).
func GetService[T any](s *Scope, key serviceKey) T {
	value, ok := TryGetService[T](s, key)
	if !ok {
		panic(fmt.Sprintf("benzene: no service registered for key %v", key))
	}
	return value
}

// TryGetService resolves key, returning ok = false if it has no registration instead of
// panicking.
func TryGetService[T any](s *Scope, key serviceKey) (T, bool) {
	var zero T
	reg, ok := s.container.lookup(key)
	if !ok {
		return zero, false
	}

	switch reg.lifetime {
	case lifetimeSingleton:
		return typedSingleton[T](s.container, key, reg), true
	case lifetimeScoped:
		return typedScoped[T](s, key, reg), true
	default: // lifetimeTransient
		value, _ := reg.factory(s).(T)
		return value, true
	}
}

// scopeContextKey is an unexported type so ContextWithScope's value can't collide with a key
// some other package puts on the same context.Context.
type scopeContextKey struct{}

// ContextWithScope returns a copy of ctx carrying scope, retrievable with ScopeFromContext.
// core-concepts.md §4 says invocation-scoped facts ride on the context "(or an accessor
// resolved from the invocation's scope)" - this is that accessor. RouterMiddleware calls this
// before invoking a handler, so a handler that needs a scoped or transient dependency (a
// singleton can simply be captured in the handler's closure at registration time) resolves it
// via ScopeFromContext(ctx) rather than needing Scope added to the Handler signature itself.
func ContextWithScope(ctx context.Context, scope *Scope) context.Context {
	return context.WithValue(ctx, scopeContextKey{}, scope)
}

// ScopeFromContext retrieves the Scope previously attached with ContextWithScope, ok = false
// if ctx carries none (e.g. in a unit test that calls a handler directly).
func ScopeFromContext(ctx context.Context) (*Scope, bool) {
	scope, ok := ctx.Value(scopeContextKey{}).(*Scope)
	return scope, ok
}

func (c *Container) lookup(key serviceKey) (registration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reg, ok := c.registrations[key]
	return reg, ok
}

// typedSingleton and typedScoped deliberately do NOT hold their mutex while calling
// reg.factory: a factory is allowed to resolve other services from the same container/scope
// (see core-concepts.md §8 - a scoped service's factory routinely needs other scoped
// services), and Go's sync.Mutex is not reentrant, so holding the lock across the factory
// call would self-deadlock the moment a factory did that. The tradeoff is a double-checked-
// locking pattern: under a genuine concurrent race for the same not-yet-created singleton/
// scoped instance, the factory may run more than once and only one result is kept - an
// acceptable cost for a small first-party DI-lite object, not a hot-path optimization target.

func typedSingleton[T any](c *Container, key serviceKey, reg registration) T {
	c.mu.Lock()
	if existing, ok := c.singletons[key]; ok {
		c.mu.Unlock()
		typed, _ := existing.(T)
		return typed
	}
	c.mu.Unlock()

	value := reg.factory(nil)

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.singletons[key]; ok {
		typed, _ := existing.(T)
		return typed
	}
	c.singletons[key] = value
	typed, _ := value.(T)
	return typed
}

func typedScoped[T any](s *Scope, key serviceKey, reg registration) T {
	s.mu.Lock()
	if existing, ok := s.scoped[key]; ok {
		s.mu.Unlock()
		typed, _ := existing.(T)
		return typed
	}
	s.mu.Unlock()

	value := reg.factory(s)

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.scoped[key]; ok {
		typed, _ := existing.(T)
		return typed
	}
	s.scoped[key] = value
	typed, _ := value.(T)
	return typed
}
