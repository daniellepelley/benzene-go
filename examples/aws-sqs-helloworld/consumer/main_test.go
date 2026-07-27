package main

import (
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/benzenetest"

	"github.com/daniellepelley/benzene-go/examples/aws-sqs-helloworld/greeting"
)

// These tests boot the real app from its composition root (newApp) and push a native SQS event
// in the front door with awssqs.SendSQS - the same harness shape every transport uses. To test
// the consumer on GCP instead, only the Send* call would change (benzenetest.SendPubSub).

func TestConsumer_ValidGreetMessageSucceeds(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := awssqs.SendSQS(t, host, benzene.NewTopic("greet"), greeting.GreetRequest{Name: "World"}, nil)

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}
}

func TestConsumer_MissingNameIsReportedAsBatchItemFailure(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := awssqs.SendSQS(t, host, benzene.NewTopic("greet"), greeting.GreetRequest{Name: ""}, nil)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != awssqs.TestMessageID {
		t.Errorf("BatchItemFailures = %v, want [{%s}]", resp.BatchItemFailures, awssqs.TestMessageID)
	}
}
