package main

import (
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awssqs"
	"github.com/daniellepelley/benzene-go/benzenetest"
)

// spyGreeter records the name it was asked to greet, so a test can prove the demo handler
// actually ran with the routed message. Swapping it in via WithServices exercises the same DI
// seam a real test uses to replace an external dependency with a fake.
type spyGreeter struct{ gotName string }

func (s *spyGreeter) Greet(name string) string {
	s.gotName = name
	return "Hi there, " + name
}

// This boots the SAME app main() runs and pushes a native SQS event through the whole Benzene
// pipeline via awssqs.SendSQS; only the SQS trigger is simulated. The spy proves the routed
// message reached the handler, and BatchItemFailures being empty proves the pipeline reported
// success.
func TestConsumer_ValidMessageRunsHandler(t *testing.T) {
	spy := &spyGreeter{}
	host := benzenetest.NewHost(newApp(), benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
		benzene.AddSingleton(b.Container, greeterKey, func(_ *benzene.Scope) Greeter { return spy })
	}))

	resp := awssqs.SendSQS(t, host, benzene.NewTopic("greet"), greetRequest{Name: "World"}, nil)

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
	}
	if spy.gotName != "World" {
		t.Errorf("greeter called with %q, want %q - handler didn't run with the routed message", spy.gotName, "World")
	}
}

// A message the handler rejects comes back as a single reported batch-item failure (so SQS
// redelivers just that message), not an error that fails the whole batch.
func TestConsumer_MissingNameIsReportedAsBatchItemFailure(t *testing.T) {
	host := benzenetest.NewHost(newApp())

	resp := awssqs.SendSQS(t, host, benzene.NewTopic("greet"), greetRequest{Name: ""}, nil)

	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != awssqs.TestMessageID {
		t.Errorf("BatchItemFailures = %v, want [{%s}]", resp.BatchItemFailures, awssqs.TestMessageID)
	}
}
