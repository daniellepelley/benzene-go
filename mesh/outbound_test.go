package mesh

import (
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestOutboundRegistry_RegisterAndTopicTypes(t *testing.T) {
	r := NewOutboundRegistry()
	topic := benzene.NewTopic("payments:capture")

	if err := RegisterOutbound[echoRequest, echoResponse](r, topic); err != nil {
		t.Fatalf("RegisterOutbound() error = %v", err)
	}

	request, response, ok := r.TopicTypes(topic)
	if !ok {
		t.Fatal("TopicTypes() ok = false, want true for a registered topic")
	}
	if request.Name() != "echoRequest" || response.Name() != "echoResponse" {
		t.Errorf("TopicTypes() = (%v, %v), want (echoRequest, echoResponse)", request, response)
	}
}

func TestOutboundRegistry_TopicTypes_Unregistered(t *testing.T) {
	r := NewOutboundRegistry()
	if _, _, ok := r.TopicTypes(benzene.NewTopic("no:such")); ok {
		t.Error("TopicTypes() ok = true, want false for an unregistered topic")
	}
}

func TestRegisterOutbound_DuplicateTopicIsAStartupError(t *testing.T) {
	r := NewOutboundRegistry()
	topic := benzene.NewTopic("payments:capture")

	if err := RegisterOutbound[echoRequest, echoResponse](r, topic); err != nil {
		t.Fatalf("first RegisterOutbound() error = %v", err)
	}
	if err := RegisterOutbound[echoRequest, echoResponse](r, topic); err == nil {
		t.Fatal("second RegisterOutbound() for the same topic should return an error, got nil")
	}
}

func TestOutboundRegistry_Topics_EmptyIsEmptyNotNil(t *testing.T) {
	r := NewOutboundRegistry()
	topics := r.Topics()
	if len(topics) != 0 {
		t.Errorf("Topics() = %v, want empty", topics)
	}
}

func TestOutboundRegistry_Topics_SortedByIDThenVersion(t *testing.T) {
	r := NewOutboundRegistry()
	must(t, RegisterOutbound[echoRequest, echoResponse](r, benzene.NewTopic("b:topic")))
	must(t, RegisterOutbound[echoRequest, echoResponse](r, benzene.NewTopic("a:topic").WithVersion("v2")))
	must(t, RegisterOutbound[echoRequest, echoResponse](r, benzene.NewTopic("a:topic")))

	got := r.Topics()
	want := []benzene.Topic{
		benzene.NewTopic("a:topic"),
		benzene.NewTopic("a:topic").WithVersion("v2"),
		benzene.NewTopic("b:topic"),
	}
	if len(got) != len(want) {
		t.Fatalf("Topics() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Topics()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
