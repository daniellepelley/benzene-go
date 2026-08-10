# Message handlers

A message handler is the one component in a Benzene service that holds your logic. It takes a typed
request, returns a typed result, and knows nothing about the transport that delivered the request —
no `net/http`, no status codes, no queue SDK. Everything else on this page exists to get a request to
the right handler and its result back out again: **topics** name what a handler serves, the
**registry** binds a topic to a handler, the **App lifecycle** wires it all together at startup, the
**container** supplies a handler's dependencies, and the **router** resolves a topic to its handler at
request time.

These are the language-neutral Benzene concepts, defined once for every port on the website — see
[Core concepts](https://benzene.app/docs/specification/core-concepts.html) for the full model. This
page won't re-explain them; it shows the Go shape and the exact symbols that implement each one. The
[getting-started guide](getting-started.md) walks the same pieces end-to-end as a runnable service; if
you haven't read it, start there.

## The handler shape

A Benzene handler is a plain function from a request to a `benzene.Result[T]`. It's a
`benzene.Handler[TReq, TRes]` (`registry.go`):

```go
type Handler[TReq, TRes any] func(ctx context.Context, req TReq) Result[TRes]
```

That's the whole contract. `TReq` and `TRes` are ordinary Go types — typically structs with JSON tags
so a transport binding can decode a request body into `TReq` and marshal the `TRes` payload back out.
The handler returns a *value*, not an error: `benzene.Ok(...)` for success and a status constructor
like `benzene.BadRequest[T](...)` for a client failure. See
[Message results](https://benzene.app/docs/specification/core-concepts.html) for the full status
vocabulary and the `benzene.Result[T]` factories (`benzene.Ok`, `benzene.CreatedResult`,
`benzene.NotFound`, `benzene.Conflict`, `benzene.ValidationError`, `benzene.ServiceUnavailable`,
`benzene.UnexpectedError`, …) — this page doesn't repeat that detail.

```go
type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}
```

Because a handler is just a function value of the right type, you can write it as a top-level function
(as above) and convert it at the registration site with `benzene.Handler[greetRequest, greetResponse](greetHandler)`.
The `context.Context` parameter carries cancellation and deadlines, and — once the router has run — the
invocation's DI scope, so a handler resolves scoped dependencies from it rather than taking them as
extra parameters (see [Dependency injection](#dependency-injection) below).

## Topics

A **topic** is the stable routing key a handler is bound to. Every transport — HTTP route, queue
message, service-to-service envelope — resolves to a topic, and a topic resolves to exactly one
handler. It's a small value type (`topic.go`):

```go
type Topic struct {
	ID      string
	Version string
}

func NewTopic(id string) Topic            // unversioned topic
func (t Topic) WithVersion(v string) Topic // a copy with a version
```

`benzene.NewTopic("greet")` is an unversioned topic. A `(ID, Version)` pair maps to at most one
handler: an unversioned topic and the same ID at `WithVersion("v2")` are two *distinct* topics that
can carry two independent handlers, so multiple versions of a contract coexist without colliding.
`Topic` is a comparable struct used directly as a map key, and its `String()` renders `id` or
`id@version`.

Registration is **explicit** — you call `benzene.Register` for each handler. There is no
reflection-based assembly or package scanning: Go has no attribute-scanning idiom like C#'s
`[Message("topic")]`, and the spec already requires explicit registration to be a first-class path in
every language regardless (see
[Core concepts §9](https://benzene.app/docs/specification/core-concepts.html)). The upshot is that the
registry is the complete, authoritative list of what a service serves, and it can't drift from the
running code.

## The registry

The `Registry` holds the `topic → handler` bindings (`registry.go`). You create one with
`benzene.NewRegistry()` — though in an ordinary app the `App` lifecycle creates it for you and hands it
to `ConfigureServices` — and bind handlers with the generic `benzene.Register` function:

```go
func Register[TReq, TRes any](r *Registry, topic Topic, handler Handler[TReq, TRes]) error
```

```go
if err := benzene.Register(
	registry,
	benzene.NewTopic("greet"),
	benzene.Handler[greetRequest, greetResponse](greetHandler),
); err != nil {
	log.Fatalf("register greet handler: %v", err)
}
```

`Register` returns an error if a handler is **already registered for that topic**. Registering two
handlers for the same `(ID, Version)` pair is a startup error, caught the moment you wire the service —
not a runtime dispatch ambiguity. That's why the composition roots in the examples treat a `Register`
error as fatal.

`Register` is generic over `TReq`/`TRes`, but the registry stores handlers in a type-erased form so a
transport binding can dispatch by topic alone without knowing the request type at compile time. At the
`Register` call site the concrete types *are* statically known, so the port captures them (via
`reflect.TypeOf`) purely for startup-time introspection — the mesh package derives JSON Schemas from
them for service self-description. Dispatch itself never touches reflection; it recovers the concrete
result through the `ResultInfo` interface.

The `Registry` also exposes read-only introspection, all backed by the same authoritative map:

- `Has(topic Topic) bool` — whether a handler is registered for a topic.
- `Topics() []Topic` — every registered topic, sorted by ID then Version. This is the enumeration
  behind service self-description (the mesh `Descriptor`).
- `TopicTypes(topic Topic) (request, response reflect.Type, ok bool)` — the request/response types
  captured at registration, for self-description; `ok` is false for an unregistered topic.

## The App lifecycle

`benzene.App[TConfig]` is the application definition: a three-phase startup lifecycle, run once, in
order (`app.go`). `TConfig` is your own configuration type — Benzene doesn't prescribe its shape; use
`struct{}` for a service with no configuration.

```go
type App[TConfig any] struct {
	GetConfiguration  func() TConfig
	ConfigureServices func(registry *Registry, container *Container, config TConfig)
	Configure         func(builder *ApplicationBuilder, config TConfig)
}
```

The three phases, and what belongs in each:

1. **`GetConfiguration`** produces the configuration object. No service resolution is available yet —
   this is where you read environment variables, files, or flags into `TConfig`.
2. **`ConfigureServices`** registers handlers on the `*Registry` (via `benzene.Register`) and their
   dependencies on the `*Container` (via `benzene.AddSingleton` and friends — see below). It receives
   the config from phase 1.
3. **`Configure`** builds the middleware pipeline against a platform-neutral `*ApplicationBuilder`,
   ending in the router. This is where transport-neutral wiring lives; the transport-specific entry
   points are attached *after* `Run` returns, by calling a binding's own constructor against the
   builder.

`ConfigureServices` and `Configure` are optional — an app with nothing to register or nothing to
configure beyond the defaults may leave either `nil`.

`Run` executes the three phases once and returns the built builder:

```go
func (a App[TConfig]) Run() *ApplicationBuilder
```

`Run` calls `GetConfiguration`, creates a fresh `Registry` and `Container`, runs `ConfigureServices`
against them, then constructs the `*ApplicationBuilder` and runs `Configure` against it. Because the
whole service boots from one `App` value, a test exercises exactly the wiring that ships — the
`benzenetest` package runs the same lifecycle in-process.

The `ApplicationBuilder` is what a transport binding reads to build its native entry point:

```go
type ApplicationBuilder struct {
	Registry  *Registry
	Container *Container
	Pipeline  *Pipeline
	ReservedNames wire.ReservedNames
}

func (b *ApplicationBuilder) UsePipeline(pipeline *Pipeline) *ApplicationBuilder
func (b *ApplicationBuilder) UseReservedNames(names wire.ReservedNames) *ApplicationBuilder
```

`Configure` calls `builder.UsePipeline(...)` to set the pipeline the transport bindings will run every
invocation through. A binding's `Use<Transport>(builder, ...)`-shaped constructor then reads
`Registry`, `Container`, and `Pipeline` off the builder to produce an `http.Handler`, a Lambda handler
function, and so on. `UseReservedNames` overrides the reserved wire header names (see
[Wire contracts §2](https://benzene.app/docs/specification/wire-contracts.html)) in one place for
every inbound binding built off the builder.

```go
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			benzene.AddSingleton(container, greetingCounterKey, func(_ *benzene.Scope) GreetingCounter {
				return &inMemoryGreetingCounter{}
			})
			if err := benzene.Register(registry, benzene.NewTopic("greet"),
				benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
				log.Fatalf("register greet handler: %v", err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(
				healthcheck.Middleware(checks),
				benzene.RouterMiddleware(builder.Registry),
			))
		},
	}
}

func main() {
	builder := newApp().Run()
	// hand builder to a transport binding, e.g. httpbinding.Handler(builder, routes())
}
```

## Dependency injection

Handlers should stay thin — depend on a port interface and push the real work into an injected
service. The `benzene.Container` is the registration set an application configures once at startup
(`scope.go`). Languages without a DI culture, Go included, MAY implement the container concept as an
explicit registry object rather than a full reflection framework, and this `Container` is exactly that
— a small first-party object, not a general-purpose DI container. You get one from the `App` lifecycle
(passed to `ConfigureServices`), or standalone with `benzene.NewContainer()`.

Services are keyed by any comparable value (`serviceKey = any`). Use a package-level unexported type or
a stable string constant as the key to avoid collisions — the helloworld example uses a
`const greetingCounterKey = "greeting-counter"`.

### Registering services

Registration functions are generic over the service type `T` and take a factory
`func(s *benzene.Scope) T`. Three lifetimes, each with an `Add*` and a `TryAdd*` variant:

```go
func AddSingleton[T any](c *Container, key serviceKey, factory func(s *Scope) T)
func AddScoped[T any](c *Container, key serviceKey, factory func(s *Scope) T)
func AddTransient[T any](c *Container, key serviceKey, factory func(s *Scope) T)

func TryAddSingleton[T any](c *Container, key serviceKey, factory func(s *Scope) T)
func TryAddScoped[T any](c *Container, key serviceKey, factory func(s *Scope) T)
func TryAddTransient[T any](c *Container, key serviceKey, factory func(s *Scope) T)
```

- **`AddSingleton`** — the factory runs at most once; the same instance is reused for every scope
  thereafter.
- **`AddScoped`** — the factory runs once per invocation scope; the instance lives and dies with that
  scope.
- **`AddTransient`** — the factory runs every time the service is resolved.
- The **`TryAdd*`** variants register only if the key has no registration yet. This is how framework
  defaults are made overridable (see
  [Core concepts §8](https://benzene.app/docs/specification/core-concepts.html)): the framework
  `TryAdd`s its default and the application's own explicit `Add*` wins.

```go
benzene.AddSingleton(container, greetingCounterKey, func(_ *benzene.Scope) GreetingCounter {
	return &inMemoryGreetingCounter{}
})
```

A factory receives the `*Scope` it's being resolved in, so a factory may resolve other services from
the same scope to build its own dependencies.

### Resolving in a handler

Each pipeline invocation gets its own `*benzene.Scope` (created per invocation via
`container.NewScope()`), and the router puts that scope on the handler's `context.Context`. Inside a
handler you retrieve it with `benzene.ScopeFromContext(ctx)` and resolve a service with
`benzene.GetService[T]`:

```go
func ScopeFromContext(ctx context.Context) (*Scope, bool)

func GetService[T any](s *Scope, key serviceKey) T
func TryGetService[T any](s *Scope, key serviceKey) (T, bool)
```

```go
func greetHandler(ctx context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}

	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[greetResponse]("no DI scope on context")
	}
	counter := benzene.GetService[GreetingCounter](scope, greetingCounterKey)

	return benzene.Ok(greetResponse{
		Greeting: "Hello, " + req.Name + "!",
		Count:    counter.Increment(),
	})
}
```

`GetService[T]` **panics** if the key has no registration — a missing required dependency is a
programming error, not a recoverable runtime condition — while `TryGetService[T]` returns `ok = false`
instead. `ScopeFromContext` returns `ok = false` when the context carries no scope, which happens when
a unit test calls a handler directly rather than through the pipeline; handle that case as the example
does.

Resolving from the scope on the context, rather than adding a `*Scope` parameter to the `Handler`
signature, is what keeps the handler shape uniform across every transport. A singleton dependency can
alternatively be captured in the handler's closure at registration time, avoiding the lookup entirely;
scoped and transient services must be resolved per invocation. `ContextWithScope(ctx, scope)` is the
lower-level accessor the router uses to attach the scope — you rarely call it directly.

## Routing

`benzene.RouterMiddleware` is the **terminal** middleware that turns a resolved topic into a handler
invocation (`router.go`):

```go
func RouterMiddleware(registry *Registry) Middleware
```

A `Pipeline` is an ordered onion of `Middleware`, built with `benzene.NewPipeline(...)`; the first
registered is outermost, and the router is conventionally registered **last** so every other
middleware wraps the dispatch (see
[Core concepts §4](https://benzene.app/docs/specification/core-concepts.html)). A middleware that
doesn't call `next` short-circuits the pipeline — that's how `healthcheck.Middleware` intercepts the
reserved health topic before the router ever sees it.

```go
builder.UsePipeline(benzene.NewPipeline(
	healthcheck.Middleware(checks),
	benzene.RouterMiddleware(builder.Registry),
))
```

When it runs, the router reads the topic off the invocation context, resolves it against the registry,
and dispatches — writing the outcome to `ic.Result`. Crucially, it never returns a Go error for an
application-level outcome; every case becomes a `Result`, so every caller reads `ic.Result`
uniformly:

- **Missing topic** → `ValidationError` (`"topic is missing"`).
- **No handler for the topic** → `NotFound`.
- **Request-conversion failure** (e.g. a body that won't unmarshal into `TReq`) → `BadRequest`.
- **Handler panic** → recovered and mapped to `ServiceUnavailable` — a handler panic MUST NOT crash
  the transport adapter (see
  [Core concepts §5](https://benzene.app/docs/specification/core-concepts.html), and
  [Wire contracts §3](https://benzene.app/docs/specification/wire-contracts.html) which defines
  `ServiceUnavailable` as the mapping for uncaught handler exceptions).

Before invoking the handler, the router attaches the invocation's `*Scope` to the handler's context
(so `ScopeFromContext` works) and converts the raw request payload into the handler's declared `TReq`.
Topic matching is exact — the ID and version travel as literal strings; any normalization a transport
wants to apply happens before dispatch.

## See also

- [Getting started](getting-started.md) — the same pieces built up into a runnable HTTP service.
- [Core concepts](https://benzene.app/docs/specification/core-concepts.html) — the language-neutral
  model: topics, results, the pipeline, the container, and the App lifecycle.
- [Wire contracts](https://benzene.app/docs/specification/wire-contracts.html) — the envelope,
  reserved header names, and the status vocabulary handlers return.
