package contractdoc

import (
	"fmt"
	"sort"
	"strings"
)

// ScopeOptions is the topic-scoping input of contract-document.md §5.2: an optional include-list
// (Topics) plus the reserved-topic policy (IncludeReserved) applied when no include-list is
// given. Mirrors the .NET reference's ClientSdkOptions.Topics/IncludeReservedTopics.
type ScopeOptions struct {
	// Topics is the include-list: when non-empty, only these topics are in scope, overriding
	// the §5.1 domain-only default entirely - naming a reserved topic here admits it regardless
	// of IncludeReserved.
	Topics []string
	// IncludeReserved, when Topics is empty, additionally admits every reserved topic
	// (contract-document.md §5.1) instead of only domain topics. Ignored when Topics is set.
	IncludeReserved bool
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
// contract-document.md §5.2. Every other field (info, events, components, messageEndpoint,
// transports) is left unchanged - this filters requests[] only, matching the reference
// implementation's TopicScope.Apply exactly (including that benzene:healthcheck, or any other
// reserved topic, gets no special case: it is excluded by default like any other reserved topic
// and admitted only by the ordinary IncludeReserved/Topics rules).
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
		var inScope bool
		if included != nil {
			inScope = included[RequestTopic(r)]
		} else {
			inScope = opts.IncludeReserved || !IsReserved(r)
		}
		if inScope {
			filtered = append(filtered, r)
		}
	}

	out := doc.clone()
	out["requests"] = toAnySlice(filtered)
	return &Document{Data: out}, nil
}

// TopicScopedProjection builds the topic-scoped (single-topic, self-contained) projection of doc
// for topic, per contract-document.md §5.3: requests holds exactly that one entry, events is
// empty, and components.schemas is narrowed to exactly the set that topic's request/response
// schemas reach (the schema closure, see closure.go). info/messageEndpoint/transports pass
// through unchanged. Fails loud (UnknownTopicsError) if the document has no such topic - the same
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
	out["components"] = map[string]any{"schemas": narrowed}
	return &Document{Data: out}, nil
}
