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
