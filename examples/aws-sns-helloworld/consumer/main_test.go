package main

import (
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awssns"
	"github.com/daniellepelley/benzene-go/benzenetest"

	"github.com/daniellepelley/benzene-go/examples/aws-sns-helloworld/greeting"
)

// These tests boot the real app from its composition root and push a native SNS notification in
// the front door with awssns.SendSNS - the same harness shape as the SQS consumer, only the
// Send* call differs. SNS has no partial-failure response, so a failed notification surfaces as
// a non-nil error (AWS's async-invoke-retry signal).

func TestConsumer_ValidGreetNotificationSucceeds(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	if err := awssns.SendSNS(t, host, benzene.NewTopic("greet"), greeting.GreetRequest{Name: "World"}, nil); err != nil {
		t.Errorf("SendSNS() error = %v, want nil", err)
	}
}

func TestConsumer_MissingNameIsReturnedAsError(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	if err := awssns.SendSNS(t, host, benzene.NewTopic("greet"), greeting.GreetRequest{Name: ""}, nil); err == nil {
		t.Error("SendSNS() error = nil, want an error for a failed notification")
	}
}
