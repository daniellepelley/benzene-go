package asyncapi_test

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/asyncapi"
	"github.com/daniellepelley/benzene-go/contract"
)

type placeOrder struct {
	ID string `json:"id"`
}

type orderPlaced struct {
	ID string `json:"id"`
}

// ExampleGenerate derives an AsyncAPI 3.0 document from a service's Contract Document. Every handled
// topic becomes a "receive" operation, with a reply channel when it returns a payload; every declared
// event (contract.RegisterEvent) becomes a "send" operation.
func ExampleGenerate() {
	registry := benzene.NewRegistry()
	_ = benzene.Register(registry, benzene.NewTopic("order:place"),
		benzene.Handler[placeOrder, orderPlaced](func(_ context.Context, _ placeOrder) benzene.Result[orderPlaced] {
			return benzene.Ok(orderPlaced{})
		}))
	outbound := contract.NewOutboundRegistry()
	contract.MustRegisterEvent[orderPlaced](outbound, benzene.NewTopic("order:placed"))
	spec, err := contract.Derive(registry, outbound, contract.Info{Service: "orders", ServiceVersion: "1.0.0"})
	if err != nil {
		panic(err)
	}

	doc := asyncapi.Generate(spec)

	fmt.Println(doc.AsyncAPI)
	fmt.Println(doc.Operations["receive_order_place"].Action, "on", doc.Operations["receive_order_place"].Channel.Ref)
	fmt.Println("reply:", doc.Operations["receive_order_place"].Reply.Channel.Ref)
	fmt.Println(doc.Operations["send_order_placed"].Action, "on", doc.Operations["send_order_placed"].Channel.Ref)
	// Output:
	// 3.0.0
	// receive on #/channels/order_place
	// reply: #/channels/order_place_response
	// send on #/channels/order_placed
}
