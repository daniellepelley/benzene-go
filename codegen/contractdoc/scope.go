package contractdoc

import (
	"fmt"
	"sort"
	"strings"
)

// ScopeOptions is the topic-scoping input of contract-document.md §5.2: an optional include-list.
// Mirrors the .NET reference's ClientSdkOptions.Topics.
type ScopeOptions struct {
	// Topics is the include-list: when non-empty, only these requests[] topics are in scope;
	// empty means every one is.
	Topics []string
}

// UnknownTopicsError is returned by ApplyScope/TopicScopedProjection when an include-list (or a
// single requested topic) names a topic the document does not have, per §5.2's fail-loud rule.
type UnknownTopicsError struct {
	// Unknown is the set of requested topics the document has no requests[] entry for.
	Unknown []string
	// Valid is the full set of topics the document actually has.
	Valid []string
}

func (e *UnknownTopicsError) Error() string {
	return fmt.Sprintf(
		"contractdoc: unknown topic(s) %s; valid topics: %s",
		strings.Join(e.Unknown, ", "), strings.Join(e.Valid, ", "),
	)
}

// ApplyScope projects doc's requests[] down to the topics in scope per opts, per
// contract-document.md §5.2. events[], sends[], components and the other document-level fields
// pass through unchanged, except contractHash and registrationHash, which are recomputed over the
// projection (§6.4: an include-list client compares its hash with the same include-list
// recomputed over the provider's document). A benzene: entry never survives (§5.1).
func ApplyScope(doc *Document, opts ScopeOptions) (*Document, error) {
	requests := doc.Requests()

	known := make(map[string]bool, len(requests))
	for _, r := range requests {
		known[RequestTopic(r)] = true
	}

	var included map[string]bool
	if len(opts.Topics) > 0 {
		included = make(map[string]bool, len(opts.Topics))
		var unknown []string
		for _, t := range opts.Topics {
			included[t] = true
			if !known[t] {
				unknown = append(unknown, t)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, &UnknownTopicsError{Unknown: unknown, Valid: doc.SortedTopics()}
		}
	}

	filtered := make([]map[string]any, 0, len(requests))
	for _, r := range requests {
		if included == nil || included[RequestTopic(r)] {
			filtered = append(filtered, r)
		}
	}

	out := doc.clone()
	out["requests"] = toAnySlice(filtered)
	return withHashes(out)
}

// TopicScopedProjection builds the topic-scoped (single-topic, self-contained) projection of doc
// for topic, per contract-document.md §5.3: requests holds exactly that one entry, events and
// sends are empty, components.schemas is narrowed to exactly the set that topic's request/response
// schemas reach (the schema closure, see closure.go), and contractHash/registrationHash are
// recomputed over the projection. The other document-level fields pass through unchanged. Fails loud (UnknownTopicsError) if the document has no such topic - the same
// rule §5.2 applies to an include-list, applied here to a single requested topic.
func TopicScopedProjection(doc *Document, topic string) (*Document, error) {
	request := doc.RequestByTopic(topic)
	if request == nil {
		return nil, &UnknownTopicsError{Unknown: []string{topic}, Valid: doc.SortedTopics()}
	}

	schemas := doc.Schemas()
	reached := ReachableSchemaNames(schemas, request["request"], request["response"])

	narrowed := make(map[string]any, len(reached))
	for name, schema := range schemas {
		if reached[name] {
			narrowed[name] = schema
		}
	}

	out := doc.clone()
	out["requests"] = []any{request}
	out["events"] = []any{}
	out["sends"] = []any{}
	out["components"] = map[string]any{"schemas": narrowed}
	return withHashes(out)
}
