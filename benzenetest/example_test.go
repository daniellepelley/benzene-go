package benzenetest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

type exampleGreetReq struct {
	Name string `json:"name"`
}

type exampleGreetResp struct {
	Greeting string `json:"greeting"`
}

type exampleOrder struct {
	ID string `json:"id"`
}

// ExampleInvoke tests a registered handler in-process, with no real HTTP/Lambda/queue: boot the
// application from its own composition root, then Invoke drives one pipeline invocation and hands
// back the typed Result to assert on.
func ExampleInvoke() {
	app := benzene.App[struct{}]{
		ConfigureServices: func(r *benzene.Registry, _ *benzene.Container, _ struct{}) {
			_ = benzene.Register(r, benzene.NewTopic("greet"),
				benzene.Handler[exampleGreetReq, exampleGreetResp](func(_ context.Context, req exampleGreetReq) benzene.Result[exampleGreetResp] {
					return benzene.Ok(exampleGreetResp{Greeting: "Hello, " + req.Name + "!"})
				}))
		},
		Configure: func(b *benzene.ApplicationBuilder, _ struct{}) {
			b.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(b.Registry)))
		},
	}
	host := benzenetest.NewHost(app)

	result := benzenetest.Invoke[exampleGreetReq, exampleGreetResp](
		context.Background(), host.Builder(), benzene.NewTopic("greet"), nil, exampleGreetReq{Name: "World"})

	fmt.Println(result.Status)
	fmt.Println(result.Payload.Greeting)
	// Output:
	// ok
	// Hello, World!
}

// exampleTB adapts a godoc Example (which receives no *testing.T) to the harness's TB seam. In a
// passing example Fatalf is never reached; if a step ever failed, the panic surfaces it loudly
// rather than letting the example continue on zero values. Real tests pass their *testing.T.
type exampleTB struct{}

func (exampleTB) Helper()                           {}
func (exampleTB) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

// Example shows the flagship flow: a native event in the front door, the handler's response out,
// and an assertion on what the service published - all against a faked outbound client. Only the
// Send* call names the transport (swap SendAPIGateway for SendSQS/SendPubSub/... and nothing else
// changes), and WithServices swaps the real Sender for a FakeMessageSender to observe egress.
func Example() {
	fake := benzenetest.NewFakeMessageSender()

	app := benzene.App[struct{}]{
		ConfigureServices: func(r *benzene.Registry, c *benzene.Container, _ struct{}) {
			client.RegisterSender(c, client.SenderFunc(func(context.Context, benzene.Topic, map[string]string, []byte) benzene.Result[json.RawMessage] {
				return benzene.Accepted[json.RawMessage](nil)
			}))
			_ = benzene.Register(r, benzene.NewTopic("order:create"),
				benzene.Handler[exampleOrder, exampleOrder](func(ctx context.Context, req exampleOrder) benzene.Result[exampleOrder] {
					scope, _ := benzene.ScopeFromContext(ctx)
					body, _ := json.Marshal(req)
					if res := client.SenderFromScope(scope).Send(ctx, benzene.NewTopic("order:created"), nil, body); !res.Status.IsSuccess() {
						return benzene.Result[exampleOrder]{Status: res.Status, Errors: res.Errors}
					}
					return benzene.Accepted(req)
				}))
		},
		Configure: func(b *benzene.ApplicationBuilder, _ struct{}) {
			b.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(b.Registry)))
		},
	}

	host := benzenetest.NewHost(app,
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			client.RegisterSender(b.Container, fake)
		}),
		benzenetest.WithRoutes(httpbinding.Route{Method: http.MethodPost, Path: "/orders", Topic: benzene.NewTopic("order:create")}),
	)

	resp := benzenetest.SendAPIGateway(exampleTB{}, host, http.MethodPost, "/orders", exampleOrder{ID: "order-1"}, nil)

	fmt.Println(resp.StatusCode)
	fmt.Println(fake.LastTopic())
	// Output:
	// 202
	// order:created
}
