package benzene

// Topic identifies a message type and routes it to a handler, per
// docs/specification/core-concepts.md §2 in the main Benzene repo (the spec this
// package implements).
//
// A (ID, Version) pair maps to at most one handler. When a message arrives without
// a version, the unversioned handler (Version == "") handles it; versioned handlers
// are selected only by an exact match.
type Topic struct {
	ID      string
	Version string
}

// NewTopic returns an unversioned Topic with the given id.
func NewTopic(id string) Topic {
	return Topic{ID: id}
}

// WithVersion returns a copy of the topic with the given version.
func (t Topic) WithVersion(version string) Topic {
	t.Version = version
	return t
}

func (t Topic) String() string {
	if t.Version == "" {
		return t.ID
	}
	return t.ID + "@" + t.Version
}
