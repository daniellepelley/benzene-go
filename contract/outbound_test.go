package contract

import (
	"reflect"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

type order struct {
	ID string `json:"id"`
}

type receipt struct {
	OK bool `json:"ok"`
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegisterEventAndSend_RecordKindAndTypes(t *testing.T) {
	r := NewOutboundRegistry()
	must(t, RegisterEvent[order](r, benzene.NewTopic("order:placed")))
	must(t, RegisterSend[order](r, benzene.NewTopic("payment:take")))
	must(t, RegisterSend[order](r, benzene.NewTopic("stock:reserve"), WithResponse[receipt]()))

	cases := []struct {
		topic    string
		kind     OutboundKind
		response reflect.Type
	}{
		{"order:placed", KindEvent, nil},
		{"payment:take", KindSend, nil},
		{"stock:reserve", KindSend, reflect.TypeOf(receipt{})},
	}
	for _, c := range cases {
		kind, ok := r.Kind(benzene.NewTopic(c.topic))
		if !ok || kind != c.kind {
			t.Errorf("%s: Kind() = (%v, %v), want (%v, true)", c.topic, kind, ok, c.kind)
		}
		req, res, ok := r.TopicTypes(benzene.NewTopic(c.topic))
		if !ok || req != reflect.TypeOf(order{}) || res != c.response {
			t.Errorf("%s: TopicTypes() = (%v, %v, %v)", c.topic, req, res, ok)
		}
	}
	if _, _, ok := r.TopicTypes(benzene.NewTopic("no:such")); ok {
		t.Error("TopicTypes() ok for an undeclared topic")
	}
	if _, ok := r.Kind(benzene.NewTopic("no:such")); ok {
		t.Error("Kind() ok for an undeclared topic")
	}
}

func TestRegister_DuplicatesAreStartupErrors(t *testing.T) {
	topic := benzene.NewTopic("order:placed").WithVersion("v1")
	cases := map[string]struct {
		first, second func(*OutboundRegistry) error
		want          string
	}{
		"event twice": {
			func(r *OutboundRegistry) error { return RegisterEvent[order](r, topic) },
			func(r *OutboundRegistry) error { return RegisterEvent[order](r, topic) },
			"event already declared",
		},
		"send twice": {
			func(r *OutboundRegistry) error { return RegisterSend[order](r, topic) },
			func(r *OutboundRegistry) error { return RegisterSend[order](r, topic) },
			"send already declared",
		},
		"event then send": {
			func(r *OutboundRegistry) error { return RegisterEvent[order](r, topic) },
			func(r *OutboundRegistry) error { return RegisterSend[order](r, topic) },
			"already declared as an event",
		},
		"send then event": {
			func(r *OutboundRegistry) error { return RegisterSend[order](r, topic) },
			func(r *OutboundRegistry) error { return RegisterEvent[order](r, topic) },
			"already declared as a send",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := NewOutboundRegistry()
			must(t, c.first(r))
			err := c.second(r)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "order:placed@v1") {
				t.Errorf("error = %v, want one containing %q and the topic", err, c.want)
			}
		})
	}
}

func TestOutboundRegistry_ZeroValueIsUsable(t *testing.T) {
	var r OutboundRegistry
	must(t, RegisterEvent[order](&r, benzene.NewTopic("a")))
	if got := r.Topics(); len(got) != 1 {
		t.Errorf("Topics() = %v", got)
	}
}

func TestOutboundRegistry_ListsAreSortedAndSplitByKind(t *testing.T) {
	r := NewOutboundRegistry()
	must(t, RegisterSend[order](r, benzene.NewTopic("b")))
	must(t, RegisterEvent[order](r, benzene.NewTopic("a").WithVersion("v2")))
	must(t, RegisterEvent[order](r, benzene.NewTopic("a")))
	must(t, RegisterSend[order](r, benzene.NewTopic("a").WithVersion("v1")))

	if got := topicStrings(r.Topics()); got != "a,a@v1,a@v2,b" {
		t.Errorf("Topics() = %s", got)
	}
	if got := topicStrings(r.Events()); got != "a,a@v2" {
		t.Errorf("Events() = %s", got)
	}
	if got := topicStrings(r.Sends()); got != "a@v1,b" {
		t.Errorf("Sends() = %s", got)
	}
	if got := NewOutboundRegistry().Topics(); got == nil || len(got) != 0 {
		t.Errorf("empty Topics() = %v, want empty non-nil", got)
	}
}

func topicStrings(topics []benzene.Topic) string {
	out := make([]string, len(topics))
	for i, topic := range topics {
		out[i] = topic.String()
	}
	return strings.Join(out, ",")
}

func TestMustRegister_PanicsWithTheError(t *testing.T) {
	r := NewOutboundRegistry()
	MustRegisterEvent[order](r, benzene.NewTopic("order:placed"))
	MustRegisterSend[order](r, benzene.NewTopic("payment:take"), WithResponse[any]())
	if _, res, _ := r.TopicTypes(benzene.NewTopic("payment:take")); res == nil || res.Kind() != reflect.Interface {
		t.Errorf("WithResponse[any]() recorded %v, want the interface type", res)
	}

	for name, call := range map[string]func(){
		"event": func() { MustRegisterEvent[order](r, benzene.NewTopic("order:placed")) },
		"send":  func() { MustRegisterSend[order](r, benzene.NewTopic("payment:take")) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				err, ok := recover().(error)
				if !ok || !strings.Contains(err.Error(), "already declared") {
					t.Errorf("panic = %v, want the registration error", err)
				}
			}()
			call()
		})
	}
}

func TestOutboundKind_String(t *testing.T) {
	if KindEvent.String() != "event" || KindSend.String() != "send" {
		t.Errorf("String() = %s, %s", KindEvent, KindSend)
	}
}
