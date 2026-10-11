package contractdoc

import (
	"errors"
	"sort"
	"testing"
)

func requestTopics(t *testing.T, doc *Document) []string {
	t.Helper()
	var topics []string
	for _, r := range doc.Requests() {
		topics = append(topics, RequestTopic(r))
	}
	sort.Strings(topics)
	return topics
}

func TestApplyScope_NoIncludeListKeepsEveryRequest(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	scoped, err := ApplyScope(doc, ScopeOptions{})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}
	assertStringSlice(t, requestTopics(t, scoped), []string{"orders:create", "orders:void"})

	// Everything else passes through unchanged - but the hashes are the projection's own.
	if len(scoped.Events()) != len(doc.Events()) || len(scoped.Sends()) != len(doc.Sends()) {
		t.Error("events and sends should be unchanged by ApplyScope")
	}
	if len(scoped.Schemas()) != len(doc.Schemas()) {
		t.Error("components should be unchanged by ApplyScope")
	}
	contractHash, registrationHash, _ := Hashes(scoped)
	if scoped.Data["contractHash"] != contractHash || scoped.Data["registrationHash"] != registrationHash {
		t.Errorf("scoped document carries (%v, %v), want its own hashes", scoped.Data["contractHash"], scoped.Data["registrationHash"])
	}
}

func TestApplyScope_IncludeListKeepsOnlyNamedRequests(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	scoped, err := ApplyScope(doc, ScopeOptions{Topics: []string{"orders:void"}})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}
	assertStringSlice(t, requestTopics(t, scoped), []string{"orders:void"})
	full, _ := Hash(doc)
	if scoped.Data["contractHash"] == full {
		t.Error("an include-list projection hashes differently from the whole service")
	}
}

func TestApplyScope_UnknownTopicFailsLoud(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	_, err := ApplyScope(doc, ScopeOptions{Topics: []string{"orders:create", "not-a-topic"}})
	var unknownErr *UnknownTopicsError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("err = %v, want *UnknownTopicsError", err)
	}
	assertStringSlice(t, unknownErr.Unknown, []string{"not-a-topic"})
	assertStringSlice(t, unknownErr.Valid, []string{"orders:create", "orders:void"})
	if unknownErr.Error() == "" {
		t.Error("Error() should not be empty")
	}
}

func TestTopicScopedProjection(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	scoped, err := TopicScopedProjection(doc, "orders:create")
	if err != nil {
		t.Fatalf("TopicScopedProjection: %v", err)
	}
	assertStringSlice(t, requestTopics(t, scoped), []string{"orders:create"})
	if len(scoped.Events()) != 0 || len(scoped.Sends()) != 0 {
		t.Errorf("events and sends should be empty in a topic-scoped projection, got %v, %v", scoped.Events(), scoped.Sends())
	}
	if h, _ := Hash(scoped); scoped.Data["contractHash"] != h {
		t.Errorf("projection carries contractHash %v, want its own %s", scoped.Data["contractHash"], h)
	}

	schemaNames := make([]string, 0, len(scoped.Schemas()))
	for name := range scoped.Schemas() {
		schemaNames = append(schemaNames, name)
	}
	assertStringSlice(t, schemaNames, []string{"CreateOrder", "OrderDto"})
}

func TestTopicScopedProjection_UnknownTopic(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	_, err := TopicScopedProjection(doc, "not-a-topic")
	var unknownErr *UnknownTopicsError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("err = %v, want *UnknownTopicsError", err)
	}
	assertStringSlice(t, unknownErr.Unknown, []string{"not-a-topic"})
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
