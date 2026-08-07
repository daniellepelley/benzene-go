package wire

import (
	"reflect"
	"testing"
)

func TestReservedNames_AccessorsDefaultWhenUnset(t *testing.T) {
	// The zero value uses the defaults; a set field is returned verbatim.
	var zero ReservedNames
	if got := zero.Topic(); got != DefaultTopicKey {
		t.Errorf("zero.Topic() = %q, want %q", got, DefaultTopicKey)
	}
	if got := zero.Correlation(); got != DefaultCorrelationKey {
		t.Errorf("zero.Correlation() = %q, want %q", got, DefaultCorrelationKey)
	}

	set := ReservedNames{TopicKey: "x-my-topic", CorrelationKey: "x-my-corr"}
	if got := set.Topic(); got != "x-my-topic" {
		t.Errorf("set.Topic() = %q, want %q", got, "x-my-topic")
	}
	if got := set.Correlation(); got != "x-my-corr" {
		t.Errorf("set.Correlation() = %q, want %q", got, "x-my-corr")
	}

	// A field left empty falls back independently of the other.
	partial := ReservedNames{TopicKey: "x-my-topic"}
	if got := partial.Correlation(); got != DefaultCorrelationKey {
		t.Errorf("partial.Correlation() = %q, want the default %q", got, DefaultCorrelationKey)
	}
}

func TestResolveMetadataTopic(t *testing.T) {
	tests := []struct {
		name        string
		metadata    map[string]string
		topicKey    string
		wantTopic   string
		wantHeaders map[string]string
	}{
		{
			name:        "topic resolves from the reserved key and is stripped",
			metadata:    map[string]string{"topic": "conformance:greet", "x-correlation-id": "abc-123"},
			topicKey:    DefaultTopicKey,
			wantTopic:   "conformance:greet",
			wantHeaders: map[string]string{"x-correlation-id": "abc-123"},
		},
		{
			name:        "remaining metadata becomes headers",
			metadata:    map[string]string{"topic": "conformance:greet", "x-correlation-id": "abc-123", "tenant": "acme"},
			topicKey:    DefaultTopicKey,
			wantTopic:   "conformance:greet",
			wantHeaders: map[string]string{"x-correlation-id": "abc-123", "tenant": "acme"},
		},
		{
			name:        "topic key is case-insensitive on read",
			metadata:    map[string]string{"Topic": "conformance:greet"},
			topicKey:    DefaultTopicKey,
			wantTopic:   "conformance:greet",
			wantHeaders: map[string]string{},
		},
		{
			name:        "a non-reserved key does not route",
			metadata:    map[string]string{"benzene-topic": "conformance:greet"},
			topicKey:    DefaultTopicKey,
			wantTopic:   "",
			wantHeaders: map[string]string{"benzene-topic": "conformance:greet"},
		},
		{
			name:        "absent topic key leaves the topic unresolved",
			metadata:    map[string]string{"x-correlation-id": "abc-123"},
			topicKey:    DefaultTopicKey,
			wantTopic:   "",
			wantHeaders: map[string]string{"x-correlation-id": "abc-123"},
		},
		{
			name:        "an overridden topic key is honoured and the default becomes a header",
			metadata:    map[string]string{"x-my-topic": "conformance:greet", "topic": "not-the-topic"},
			topicKey:    "x-my-topic",
			wantTopic:   "conformance:greet",
			wantHeaders: map[string]string{"topic": "not-the-topic"},
		},
		{
			name:        "nil metadata yields an empty non-nil header map",
			metadata:    nil,
			topicKey:    DefaultTopicKey,
			wantTopic:   "",
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topic, headers := ResolveMetadataTopic(tt.metadata, tt.topicKey)
			if topic != tt.wantTopic {
				t.Errorf("topic = %q, want %q", topic, tt.wantTopic)
			}
			if headers == nil {
				t.Errorf("headers = nil, want a non-nil map")
			}
			if !reflect.DeepEqual(headers, tt.wantHeaders) {
				t.Errorf("headers = %v, want %v", headers, tt.wantHeaders)
			}
		})
	}
}
