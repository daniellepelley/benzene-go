package resilience_test

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/resilience"
)

// ExampleFallback shows graceful degradation: when the downstream fails (here an unsuccessful
// service-unavailable result), the fallback substitutes a degraded result - a cached value, a default,
// a stale response - instead of surfacing the failure. Placed above a circuit breaker it also
// substitutes for the open-state fail-fast, so a tripped breaker degrades to this cached answer.
func ExampleFallback() {
	mw := resilience.Fallback(func(_ context.Context, ic *benzene.InvocationContext, cause error) error {
		ic.Result = benzene.Ok("cached: last known price")
		return nil
	})

	ic := benzene.NewInvocationContext(benzene.NewTopic("price:get"), nil, nil, nil)
	// The downstream reports the dependency is down.
	_ = mw(context.Background(), ic, func(context.Context) error {
		ic.Result = benzene.ServiceUnavailable[any]("pricing service is down")
		return nil
	})

	fmt.Println(ic.Result.ResultStatus(), "-", ic.Result.ResultPayload())
	// Output: ok - cached: last known price
}
