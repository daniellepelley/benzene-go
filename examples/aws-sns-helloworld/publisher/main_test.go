package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/client"

	"github.com/daniellepelley/benzene-go/examples/aws-sns-helloworld/greeting"
)

// These tests boot the real app from its composition root, push an API Gateway request in the
// front door, and assert on BOTH the native HTTP response AND the egress captured by a
// FakeMessageSender. WithServices swaps the real SNS client for the fake; last-registration-wins.

// wiredButOverridden stands in for the real awssns.Client the composition root would wire. The
// test overrides it via WithServices, and this fatal proves the override won.
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

func TestPublisher_TopicFailureIsServiceUnavailable(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender().WithResult(benzene.ServiceUnavailable[json.RawMessage]("boom"))
	host := newTestHost(t, fake)

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greeting.GreetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("statusCode = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}
