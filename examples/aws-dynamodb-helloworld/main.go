// Command aws-dynamodb-helloworld is a DynamoDB Streams consumer Lambda: it reacts to writes on
// an `orders` table by handling the change records the stream delivers, one Benzene topic per
// change type ("orders:INSERT", "orders:MODIFY", "orders:REMOVE"). There is no publisher here -
// the "publish" is an ordinary write to the DynamoDB table (with `PutItem`, the console, or any
// other client); the stream turns that write into an event this Lambda consumes.
//
// It is the DynamoDB counterpart of examples/aws-sqs-helloworld's consumer, and lives in the
// root module (not its own like aws-sqs-helloworld) because the awsdynamodb binding is itself
// zero-dependency: AWS delivers the change records as plain JSON to the invocation, so there is
// no AWS SDK to pull in.
package main

import (
	"context"
	"log"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awsdynamodb"
	"github.com/daniellepelley/benzene-go/awslambda"
)

// order is one row of the `orders` table, as it arrives in a stream record's image - already
// unmarshalled from DynamoDB AttributeValue format into plain JSON by the binding, so the handler
// deserializes an ordinary struct and never sees `{"S":...}`/`{"N":...}` wrappers.
type order struct {
	ID     string  `json:"id"`
	Item   string  `json:"item"`
	Amount float64 `json:"amount"`
}

// orderInserted handles the INSERT change record for a new `orders` row (topic "orders:INSERT").
// A real service would fulfil the order, charge the customer, publish a downstream event, etc;
// here it just logs. Returning a failure (a bad row) reports that record's SequenceNumber as a
// partial batch failure, so Lambda redelivers from there rather than checkpointing past it.
func orderInserted(_ context.Context, o order) benzene.Result[struct{}] {
	if o.ID == "" {
		return benzene.BadRequest[struct{}]("order id is required")
	}
	log.Printf("order placed: id=%s item=%q amount=%.2f", o.ID, o.Item, o.Amount)
	return benzene.Ok(struct{}{})
}

// newApp is the composition root both main() and the tests boot from. Only INSERT is handled; a
// MODIFY or REMOVE record routes to no handler and comes back not-found (unsuccessful), which for
// an at-least-once CDC stream means "left for redelivery" - register those topics too to consume
// them.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("orders:INSERT"), orderInserted)
		},
	}
}

func main() {
	awslambda.Start(awsdynamodb.Handler(newApp().Run()))
}
