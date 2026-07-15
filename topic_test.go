package benzene

import "testing"

func TestNewTopic(t *testing.T) {
	topic := NewTopic("order:create")
	if topic.ID != "order:create" {
		t.Errorf("ID = %q, want %q", topic.ID, "order:create")
	}
	if topic.Version != "" {
		t.Errorf("Version = %q, want empty", topic.Version)
	}
}

func TestTopic_WithVersion(t *testing.T) {
	base := NewTopic("order:create")
	versioned := base.WithVersion("v2")

	if versioned.ID != "order:create" {
		t.Errorf("ID = %q, want %q", versioned.ID, "order:create")
	}
	if versioned.Version != "v2" {
		t.Errorf("Version = %q, want %q", versioned.Version, "v2")
	}
	if base.Version != "" {
		t.Errorf("WithVersion mutated the receiver: base.Version = %q, want empty", base.Version)
	}
}

func TestTopic_String(t *testing.T) {
	tests := []struct {
		name  string
		topic Topic
		want  string
	}{
		{"unversioned", NewTopic("order:create"), "order:create"},
		{"versioned", NewTopic("order:create").WithVersion("v2"), "order:create@v2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.topic.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTopic_EqualityAsMapKey(t *testing.T) {
	// Topic must be comparable (usable as a map key) since Registry keys on it directly.
	a := NewTopic("order:create")
	b := NewTopic("order:create")
	c := NewTopic("order:create").WithVersion("v2")

	m := map[Topic]string{a: "unversioned"}
	if _, ok := m[b]; !ok {
		t.Error("identical (id, version) topics should be equal as map keys")
	}
	if _, ok := m[c]; ok {
		t.Error("different versions should not collide as map keys")
	}
}
