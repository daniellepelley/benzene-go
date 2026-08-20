package gcppubsubclient

import (
	"context"
	"errors"
	"reflect"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// fakePublisher records the last Publish call and returns a canned server id / error. It replaces
// the cloud.google.com/go/pubsub SDK entirely, so these tests need no live Pub/Sub and never
// import the SDK.
type fakePublisher struct {
	gotTopicID    string
	gotData       []byte
	gotAttributes map[string]string
	serverID      string
	err           error
	calls         int
}

func (f *fakePublisher) Publish(_ context.Context, topicID string, data []byte, attributes map[string]string) (string, error) {
	f.calls++
	f.gotTopicID = topicID
	f.gotData = data
	f.gotAttributes = attributes
	return f.serverID, f.err
}

func TestSend(t *testing.T) {
	tests := []struct {
		name          string
		reserved      wire.ReservedNames
		topic         string
		headers       map[string]string
		message       []byte
		publishErr    error
		wantStatus    benzene.Status
		wantAttrs     map[string]string
		wantErrSubstr string
	}{
		{
			name:       "success carries topic attribute and body",
			topic:      "order:create",
			message:    []byte(`{"id":1}`),
			wantStatus: benzene.StatusAccepted,
			wantAttrs:  map[string]string{"topic": "order:create"},
		},
		{
			name:       "headers become attributes alongside the topic",
			topic:      "order:create",
			headers:    map[string]string{"x-correlation-id": "abc", "tenant": "acme"},
			message:    []byte("body"),
			wantStatus: benzene.StatusAccepted,
			wantAttrs: map[string]string{
				"topic":            "order:create",
				"x-correlation-id": "abc",
				"tenant":           "acme",
			},
		},
		{
			name:       "empty header value is dropped",
			topic:      "t",
			headers:    map[string]string{"keep": "v", "drop": ""},
			message:    []byte("b"),
			wantStatus: benzene.StatusAccepted,
			wantAttrs:  map[string]string{"topic": "t", "keep": "v"},
		},
		{
			name:       "custom reserved topic key is honored",
			reserved:   wire.ReservedNames{TopicKey: "benzene-topic"},
			topic:      "order:create",
			message:    []byte("b"),
			wantStatus: benzene.StatusAccepted,
			wantAttrs:  map[string]string{"benzene-topic": "order:create"},
		},
		{
			name:          "publish error maps to service-unavailable",
			topic:         "t",
			message:       []byte("b"),
			publishErr:    errors.New("boom"),
			wantStatus:    benzene.StatusServiceUnavailable,
			wantErrSubstr: "gcppubsubclient: publish failed: boom",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakePublisher{serverID: "srv-1", err: tc.publishErr}
			c := NewClient(fake, "my-topic")
			c.ReservedNames = tc.reserved

			result := c.Send(context.Background(), benzene.NewTopic(tc.topic), tc.headers, tc.message)

			if result.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, tc.wantStatus)
			}
			if fake.calls != 1 {
				t.Fatalf("publisher called %d times, want 1", fake.calls)
			}
			if fake.gotTopicID != "my-topic" {
				t.Errorf("topicID = %q, want %q", fake.gotTopicID, "my-topic")
			}
			if string(fake.gotData) != string(tc.message) {
				t.Errorf("data = %q, want %q", fake.gotData, tc.message)
			}
			if tc.wantAttrs != nil && !reflect.DeepEqual(fake.gotAttributes, tc.wantAttrs) {
				t.Errorf("attributes = %v, want %v", fake.gotAttributes, tc.wantAttrs)
			}
			if tc.wantErrSubstr != "" {
				if len(result.Errors) != 1 || result.Errors[0].Message != tc.wantErrSubstr {
					t.Errorf("errors = %v, want [%q]", result.Errors, tc.wantErrSubstr)
				}
			}
		})
	}
}
