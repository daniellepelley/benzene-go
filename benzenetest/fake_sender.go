package benzenetest

import (
	"context"
	"encoding/json"
	"sync"

	benzene "github.com/daniellepelley/benzene-go"
)

// FakeMessageSender is an in-memory stand-in for the outbound client (client.Sender): instead of
// publishing to a real queue/topic it records the last call, so a test can assert on the egress
// side of ingress -> handler -> egress. It is the Go counterpart of the reference
// FakeBenzeneMessageSender.
//
// Register it over the real client with WithServices + client.RegisterSender, then assert on
// LastTopic / LastMessage / LastHeaders after driving a native event in the front door:
//
//	fake := benzenetest.NewFakeMessageSender()
//	host := benzenetest.NewHost(app, benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
//	    client.RegisterSender(b.Container, fake)
//	}))
//	// ... SendAPIGateway / SendSQS / ... then:
//	require... fake.LastTopic() == benzene.NewTopic("orders:created")
//
// It satisfies client.Sender structurally; the compile-time assertion lives in the client
// package's tests to avoid this package importing client just for the check.
type FakeMessageSender struct {
	mu          sync.Mutex
	calls       int
	lastTopic   benzene.Topic
	lastMessage []byte
	lastHeaders map[string]string
	result      benzene.Result[json.RawMessage]
}

// NewFakeMessageSender returns a FakeMessageSender that reports every send as Accepted - the
// same status the real SQS/SNS clients return for a successful publish - so a handler that
// checks the publish result sees success by default. Override with WithResult for failure paths.
func NewFakeMessageSender() *FakeMessageSender {
	return &FakeMessageSender{result: benzene.Accepted[json.RawMessage](nil)}
}

// WithResult sets the Result every Send returns, for exercising a handler's publish-failure path
// (e.g. benzene.ServiceUnavailable[json.RawMessage]("down")). Returns the sender for chaining.
func (f *FakeMessageSender) WithResult(result benzene.Result[json.RawMessage]) *FakeMessageSender {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = result
	return f
}

// Send records the call and returns the configured result without sending anything. It is safe
// for concurrent use, so a thread-safety test can drive it from several goroutines.
func (f *FakeMessageSender) Send(_ context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastTopic = topic
	f.lastMessage = append([]byte(nil), message...)
	f.lastHeaders = headers
	return f.result
}

// Calls returns the number of Send calls recorded.
func (f *FakeMessageSender) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// LastTopic returns the topic of the most recent Send.
func (f *FakeMessageSender) LastTopic() benzene.Topic {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTopic
}

// LastMessage returns the raw body of the most recent Send.
func (f *FakeMessageSender) LastMessage() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMessage
}

// LastHeaders returns the headers of the most recent Send.
func (f *FakeMessageSender) LastHeaders() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastHeaders
}

// DecodeLastMessage unmarshals the most recent Send's body into v, failing the test on a JSON
// error - the egress counterpart of asserting a native response body, so a test can prove the
// payload handed to the handler is what got published, not only the topic.
func (f *FakeMessageSender) DecodeLastMessage(t TB, v any) {
	t.Helper()
	f.mu.Lock()
	body := f.lastMessage
	f.mu.Unlock()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("benzenetest: decode last published message: %v; body = %s", err, body)
	}
}
