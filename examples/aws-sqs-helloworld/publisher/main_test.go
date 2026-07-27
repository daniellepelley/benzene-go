package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/client"

	"github.com/daniellepelley/benzene-go/examples/aws-sqs-helloworld/greeting"
)

// These tests boot the real app from its composition root, push an API Gateway request in the
// front door, and assert on BOTH the native HTTP response AND the egress captured by a
// FakeMessageSender - the ingress -> handler -> egress shape. WithServices swaps the real SQS
// client for the fake; last-registration-wins over the sender the composition root registered.

// wiredButOverridden stands in for the real awssqs.Client the composition root would wire in a
// deployment. The test overrides it via WithServices, and this fatal proves the override won -
// the handler never reaches the "real" client.
func wiredButOverridden(t *testing.T) client.Sender {
	return client.SenderFunc(func(context.Context, benzene.Topic, map[string]string, []byte) benzene.Result[json.RawMessage] {
		t.Fatal("the real Sender should have been overridden by WithServices")
		return benzene.Result[json.RawMessage]{}
	})
}

func newTestHost(t *testing.T, fake *benzenetest.FakeMessageSender) *benzenetest.Host {
	t.Helper()
	return benzenetest.NewHost(newApp(wiredButOverridden(t)),
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			client.RegisterSender(b.Container, fake)
		}),
		benzenetest.WithRoutes(routes()...),
	)
}

func TestPublisher_ForwardsToTopicAndReturnsAccepted(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender()
	host := newTestHost(t, fake)

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greeting.GreetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("statusCode = %d, want %d; body = %s", resp.StatusCode, http.StatusAccepted, resp.Body)
	}
	if got := fake.LastTopic(); got != benzene.NewTopic("greet") {
		t.Errorf("LastTopic = %v, want greet", got)
	}
	var sent greeting.GreetRequest
	fake.DecodeLastMessage(t, &sent)
	if sent.Name != "World" {
		t.Errorf("published Name = %q, want %q", sent.Name, "World")
	}
}

func TestPublisher_DoesNotValidateContentBeforeForwarding(t *testing.T) {
	// The publisher's job is only to forward to the queue - it deliberately does not duplicate
	// the consumer's own validation. An empty name is still accepted here and published; it's the
	// consumer that later reports the actual validation failure, invisible to this HTTP caller.
	fake := benzenetest.NewFakeMessageSender()
	host := newTestHost(t, fake)

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greeting.GreetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("statusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if fake.Calls() != 1 {
		t.Errorf("Calls = %d, want 1 (forwarded despite empty name)", fake.Calls())
	}
}

func TestPublisher_QueueFailureIsServiceUnavailable(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender().WithResult(benzene.ServiceUnavailable[json.RawMessage]("boom"))
	host := newTestHost(t, fake)

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greeting.GreetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("statusCode = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}
