// Command aws-kinesis-helloworld is a Kinesis Data Streams consumer Lambda: it reacts to records
// on an `orders` stream by handling each one. There is no publisher here - the "publish" is a
// `PutRecord` to the Kinesis stream (with the AWS CLI, an SDK, or any producer); the stream's event
// source mapping turns each record into an invocation this Lambda consumes.
//
// It is the Kinesis counterpart of examples/aws-dynamodb-helloworld, and lives in the root module
// (not its own like aws-sqs-helloworld) because the awskinesis binding is itself zero-dependency:
// AWS delivers the records as plain JSON (the payload base64-encoded inside), so there is no AWS SDK
// to pull in.
package main

import (
	"context"
	"log"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awskinesis"
	"github.com/daniellepelley/benzene-go/awslambda"
)

// order is one record on the `orders` stream, as it arrives after the binding base64-decodes the
// record data back to the JSON the producer wrote.
type order struct {
	ID     string  `json:"id"`
	Item   string  `json:"item"`
	Amount float64 `json:"amount"`
}

// orderReceived handles a record from the `orders` stream (the topic is the stream name). A record
// the handler rejects reports that record's SequenceNumber as a partial batch failure, so AWS
// resumes the shard from there rather than checkpointing past it.
func orderReceived(_ context.Context, o order) benzene.Result[struct{}] {
	if o.ID == "" {
		return benzene.BadRequest[struct{}]("order id is required")
	}
	log.Printf("order received: id=%s item=%q amount=%.2f", o.ID, o.Item, o.Amount)
	return benzene.Ok(struct{}{})
}

// newApp is the composition root both main() and the tests boot from. The topic is the stream name
// ("orders"); a Kinesis record has no per-record event type, so the stream itself is the routing
// key (unlike DynamoDB Streams' "{table}:{event}").
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("orders"), orderReceived)
		},
	}
}

func main() {
	awslambda.Start(awskinesis.Handler(newApp().Run()))
}
