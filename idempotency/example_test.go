package idempotency_test

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/idempotency"
)

// ExampleMiddleware shows de-duplication of a redelivered message on an at-least-once transport. The
// key is read from a header; the first delivery claims it and runs the handler, and a redelivery of
// the same key short-circuits to ignored (an ack) without running the handler again.
func ExampleMiddleware() {
	store := idempotency.NewInMemoryStore()
	key := func(ic *benzene.InvocationContext) string { return ic.Headers["message-id"] }
	mw := idempotency.Middleware(store, key)

	handled := 0
	deliver := func() benzene.Status {
		ic := benzene.NewInvocationContext(benzene.NewTopic("order:create"),
			map[string]string{"message-id": "msg-1"}, nil, nil)
		_ = mw(context.Background(), ic, func(context.Context) error {
			handled++
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
		return ic.Result.ResultStatus()
	}

	fmt.Println("1st delivery:", deliver())
	fmt.Println("redelivery:  ", deliver())
	fmt.Println("handler ran:", handled, "time(s)")
	// Output:
	// 1st delivery: ok
	// redelivery:   ignored
	// handler ran: 1 time(s)
}
