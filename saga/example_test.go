package saga_test

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/saga"
)

// ExampleSaga_rollback shows the compensating-transaction pattern: stages run in order, and when a
// later stage fails, every completed effect is compensated in reverse (LIFO) order. Here "reserve
// stock" succeeds, "charge card" fails, so the reservation is released and the outcome is rolled-back.
// Each NewStep pairs a forward action (producing a typed result) with the compensation that undoes it.
func ExampleSaga_rollback() {
	var log []string

	reserve := saga.NewStep(
		func(_ context.Context, _ *saga.SagaContext) benzene.Result[string] {
			log = append(log, "reserve stock")
			return benzene.Ok("reservation-1")
		},
		func(_ context.Context, _ *saga.SagaContext, id string) benzene.Result[struct{}] {
			log = append(log, "release "+id)
			return benzene.Ok(struct{}{})
		},
	)

	charge := saga.NewStep(
		func(_ context.Context, _ *saga.SagaContext) benzene.Result[string] {
			log = append(log, "charge card (fails)")
			return benzene.ServiceUnavailable[string]("payment gateway down")
		},
		func(_ context.Context, _ *saga.SagaContext, _ string) benzene.Result[struct{}] {
			return benzene.Ok(struct{}{}) // never reached - the forward never produced an effect
		},
	)

	result := saga.New(saga.NewStage(reserve), saga.NewStage(charge)).Run(context.Background())

	fmt.Println("outcome:", result.Outcome)
	for _, entry := range log {
		fmt.Println("-", entry)
	}
	// Output:
	// outcome: rolled-back
	// - reserve stock
	// - charge card (fails)
	// - release reservation-1
}
