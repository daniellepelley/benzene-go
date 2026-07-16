# benzenetest

An in-process test host for applications built on `benzene-go` - the Go counterpart to the main
[daniellepelley/Benzene](https://github.com/daniellepelley/Benzene) repo's `Benzene.Testing` /
`BenzeneTestHost`. Use it in *your own* application's tests to exercise a registered handler
through the full pipeline (middleware included) without spinning up real HTTP, Lambda, or Azure
Functions.

```go
result := benzenetest.Invoke[GreetRequest, GreetResponse](
	context.Background(),
	builder, // your *benzene.ApplicationBuilder, exactly as App.Run() returns it
	benzene.NewTopic("greet"),
	nil, // headers
	GreetRequest{Name: "World"},
)

if result.Status != benzene.StatusOk {
	t.Fatalf("Status = %q, want Ok", result.Status)
}
if result.Payload.Greeting != "Hello, World!" {
	t.Errorf("Greeting = %q", result.Payload.Greeting)
}
```

`request` is passed straight through as the raw request value - no JSON round-trip - so
middleware, DI resolution (`benzene.ScopeFromContext`), and the router's own dispatch all run
for real, exactly as they would for a live request. A pipeline error or a missing result becomes
`ServiceUnavailable`/`UnexpectedError` respectively, matching every other binding in this repo's
"every outcome is a `Result`, never a raw error" rule - so a test always gets one `Result` to
assert on.

This is for testing *your* handlers and pipeline wiring. This repo's own test suite doesn't use
it - it builds an `InvocationContext` directly wherever that's the clearer choice for testing
this library's own internals.
