package rabbitmq

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

func TestConsumer_Settlement(t *testing.T) {
	run := func(settlement envelope.Settlement) (*fakeAcknowledger, int) {
		t.Helper()
		ack := &fakeAcknowledger{}
		failures := 0
		consumer := &Consumer{
			Source: &fakeSource{deliveries: []amqp.Delivery{
				delivery(ack, 1, amqp.Table{"topic": []byte("greet")}, "", `{"name":""}`),
				delivery(ack, 2, amqp.Table{"topic": []byte("someone:elses")}, "", `{}`),
			}},
			Builder:    newTestBuilder(t),
			Settlement: settlement,
			OnFailure:  func(context.Context, amqp.Delivery, wire.Response) { failures++ },
		}
		_ = consumer.Run(context.Background()) // the fake closes the stream once drained
		return ack, failures
	}

	ack, failures := run(envelope.Settlement{Delivery: envelope.AtMostOnce})
	if len(ack.acks) != 2 || len(ack.nacks) != 0 || failures != 2 {
		t.Errorf("at-most-once acks %v nacks %v failures %d, want both acked and both reported", ack.acks, ack.nacks, failures)
	}

	ack, _ = run(envelope.Settlement{IgnoreUnmatchedTopics: true})
	if len(ack.acks) != 1 || ack.acks[0] != 2 || len(ack.nacks) != 1 || ack.nacks[0].tag != 1 {
		t.Errorf("IgnoreUnmatchedTopics acks %v nacks %v, want the unmatched delivery acked and the failure nacked", ack.acks, ack.nacks)
	}
}
