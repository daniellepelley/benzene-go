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

func TestApplyScope_DomainOnlyDefault(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	scoped, err := ApplyScope(doc, ScopeOptions{})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}
	got := requestTopics(t, scoped)
	want := []string{"orders:create"}
	assertStringSlice(t, got, want)

	// Everything else passes through unchanged.
	if len(scoped.Events()) != len(doc.Events()) {
		t.Error("events should be unchanged by ApplyScope")
	}
	if len(scoped.Schemas()) != len(doc.Schemas()) {
		t.Error("components should be unchanged by ApplyScope")
	}
}

func TestApplyScope_IncludeReserved(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	scoped, err := ApplyScope(doc, ScopeOptions{IncludeReserved: true})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}
	assertStringSlice(t, requestTopics(t, scoped), []string{"benzene:healthcheck", "benzene:spec", "orders:create"})
}

func TestApplyScope_ExplicitTopicListAdmitsReservedWithoutIncludeReserved(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	scoped, err := ApplyScope(doc, ScopeOptions{Topics: []string{"benzene:spec"}})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}
	assertStringSlice(t, requestTopics(t, scoped), []string{"benzene:spec"})
}

func TestApplyScope_ExplicitListDoesNotImplicitlyAdmitIncludeReservedTopics(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	scoped, err := ApplyScope(doc, ScopeOptions{Topics: []string{"orders:create"}, IncludeReserved: true})
	if err != nil {
		t.Fatalf("ApplyScope: %v", err)
	}
	assertStringSlice(t, requestTopics(t, scoped), []string{"orders:create"})
}

func TestApplyScope_UnknownTopicFailsLoud(t *testing.T) {
	doc := mustParse(t, minimalDoc)
	_, err := ApplyScope(doc, ScopeOptions{Topics: []string{"orders:create", "not-a-topic"}})
	var unknownErr *UnknownTopicsError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("err = %v, want *UnknownTopicsError", err)
	}
	assertStringSlice(t, unknownErr.Unknown, []string{"not-a-topic"})
	assertStringSlice(t, unknownErr.Valid, []string{"benzene:healthcheck", "benzene:spec", "orders:create"})
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
	if len(scoped.Events()) != 0 {
		t.Errorf("events should be empty in a topic-scoped projection, got %v", scoped.Events())
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
