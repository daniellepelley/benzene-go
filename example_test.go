package benzene_test

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
)

type greetReq struct {
	Name string `json:"name"`
}

type greetResp struct {
	Greeting string `json:"greeting"`
}

// ExampleRegister shows the core loop: a handler is a plain func(context, TReq) Result[TRes];
// Register binds it to a topic on a Registry; RouterMiddleware turns the Registry into a Pipeline
// that dispatches an incoming message to the matching handler. (In a real service a transport
// binding - httpbinding, awslambda, a queue Consumer - feeds the pipeline; here we drive it
// directly to show the moving parts.)
func ExampleRegister() {
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"),
		benzene.Handler[greetReq, greetResp](func(_ context.Context, req greetReq) benzene.Result[greetResp] {
			return benzene.Ok(greetResp{Greeting: "Hello, " + req.Name + "!"})
		})); err != nil {
		panic(err)
	}

	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))
	scope := benzene.NewContainer().NewScope()
	ic := benzene.NewInvocationContext(benzene.NewTopic("greet"), nil, greetReq{Name: "World"}, scope)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		panic(err)
	}

	fmt.Println(ic.Result.ResultStatus())
	fmt.Println(ic.Result.ResultPayload().(greetResp).Greeting)
	// Output:
	// ok
	// Hello, World!
}

// ExampleResult shows how a handler signals its outcome with the shared, wire-level status
// vocabulary rather than a Go error: Ok for success and BadRequest/NotFound/... for the failure
// modes. Every transport maps the same status the same way (an HTTP code, a gRPC code, a queue
// ack/nack), so the handler names the outcome once and stays transport-agnostic.
func ExampleResult() {
	lookup := func(_ context.Context, req greetReq) benzene.Result[greetResp] {
		switch req.Name {
		case "":
			return benzene.BadRequest[greetResp]("name is required")
		case "nobody":
			return benzene.NotFound[greetResp]("no such user")
		default:
			return benzene.Ok(greetResp{Greeting: "Hello, " + req.Name + "!"})
		}
	}

	for _, name := range []string{"World", "", "nobody"} {
		result := lookup(context.Background(), greetReq{Name: name})
		fmt.Printf("%-8q -> %s\n", name, result.ResultStatus())
	}
	// Output:
	// "World"  -> ok
	// ""       -> bad-request
	// "nobody" -> not-found
}

type greetingCount struct{ n int }

// countKey is a typed DI key (a struct key can't collide with another package's key the way a bare
// string could).
type countKey struct{}

// ExampleGetService shows the DI-lite Container/Scope. Register a per-invocation (scoped) dependency
// under a typed key; a handler then resolves it from the context via ScopeFromContext + GetService.
// Resolving twice inside one scope returns the same instance. (For a singleton you don't need the
// container at all - capture it in the handler's closure at registration time.)
func ExampleGetService() {
	container := benzene.NewContainer()
	benzene.AddScoped(container, countKey{}, func(*benzene.Scope) *greetingCount {
		return &greetingCount{}
	})

	scope := container.NewScope() // one scope per invocation; a transport binding creates it for you
	first := benzene.GetService[*greetingCount](scope, countKey{})
	first.n++
	second := benzene.GetService[*greetingCount](scope, countKey{})

	fmt.Println(first == second, second.n)
	// Output: true 1
}

// ExampleRouterMiddleware_versioned shows inbound handler-version dispatch. Two handlers register
// for the same topic id under different versions; the router reads the message's benzene-version
// header off the wire and dispatches to the exact match. A message with no version header routes
// to the unversioned handler (the default version), and so does one whose version has no exact
// handler - so turning versioning on for a topic never breaks a producer that doesn't send one.
func ExampleRouterMiddleware_versioned() {
	registry := benzene.NewRegistry()
	mustRegister := func(topic benzene.Topic, greeting string) {
		if err := benzene.Register(registry, topic,
			benzene.Handler[greetReq, greetResp](func(_ context.Context, req greetReq) benzene.Result[greetResp] {
				return benzene.Ok(greetResp{Greeting: greeting + req.Name})
			})); err != nil {
			panic(err)
		}
	}
	mustRegister(benzene.NewTopic("greet"), "Hello ")               // the default (unversioned) handler
	mustRegister(benzene.NewTopic("greet").WithVersion("2"), "Hi ") // the v2 handler

	pipeline := benzene.NewPipeline(benzene.RouterMiddleware(registry))
	greet := func(headers map[string]string) string {
		ic := benzene.NewInvocationContext(benzene.NewTopic("greet"), headers, greetReq{Name: "World"}, nil)
		if err := pipeline.Run(context.Background(), ic); err != nil {
			panic(err)
		}
		return ic.Result.ResultPayload().(greetResp).Greeting
	}

	fmt.Println(greet(map[string]string{"benzene-version": "2"})) // exact match -> v2
	fmt.Println(greet(nil))                                       // no version -> default
	fmt.Println(greet(map[string]string{"benzene-version": "9"})) // unknown version -> default (non-regressive)
	// Output:
	// Hi World
	// Hello World
	// Hello World
}
