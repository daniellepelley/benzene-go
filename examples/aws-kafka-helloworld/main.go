// Command aws-kafka-helloworld is a Kafka consumer Lambda: it reacts to records on an `orders`
// Kafka topic by handling each one. There is no publisher here - the "publish" is producing a
// record to the Kafka topic (with any Kafka producer); an Amazon MSK or self-managed-Kafka event
// source mapping turns each record into an invocation this Lambda consumes.
//
// It is the Kafka counterpart of examples/aws-kinesis-helloworld, and lives in the root module (not
// its own like aws-sqs-helloworld) because the awskafka binding is itself zero-dependency: AWS
// delivers the records as plain JSON (the value base64-encoded inside), so there is no AWS SDK to
// pull in. It is DISTINCT from the self-hosted `kafka` module, which runs its own broker consumer
// loop (and needs segmentio/kafka-go); this is the managed-Lambda adapter.
package main

import (
	"context"
	"log"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awskafka"
	"github.com/daniellepelley/benzene-go/awslambda"
)

// order is one record on the `orders` topic, as it arrives after the binding base64-decodes the
// record value back to the JSON the producer wrote.
type order struct {
	ID     string  `json:"id"`
	Item   string  `json:"item"`
	Amount float64 `json:"amount"`
}

// orderReceived handles a record from the `orders` topic (the Kafka topic is the Benzene topic
// verbatim). A record the handler rejects reports that partition's {partition, offset} as a partial
// batch failure, so AWS resumes that partition from there rather than committing past it.
func orderReceived(_ context.Context, o order) benzene.Result[struct{}] {
	if o.ID == "" {
		return benzene.BadRequest[struct{}]("order id is required")
	}
	log.Printf("order received: id=%s item=%q amount=%.2f", o.ID, o.Item, o.Amount)
	return benzene.Ok(struct{}{})
}

// newApp is the composition root both main() and the tests boot from. The topic is the Kafka topic
// name ("orders") - one Kafka topic maps to one Benzene topic, verbatim.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			benzene.MustRegister(registry, benzene.NewTopic("orders"), orderReceived)
		},
	}
}

func main() {
	awslambda.Start(awskafka.Handler(newApp().Run()))
}
