// Package gengo turns a (possibly already topic-scoped) contractdoc.Document into idiomatic Go
// source: one package per generated client - a struct/DTO per reachable component schema,
// exported PascalCase methods (one per in-scope request topic), an embedded contractHash, and a
// RequiredTopics var - depending only on this port's transport-agnostic client.Sender,
// httpclient.Unmarshal and benzene.Result[T]/benzene.Topic (contract-document.md §5.4). Naming
// and file layout are explicitly out of conformance scope (§5.5); the choices here are this
// port's own idiom, documented package-by-package below.
package gengo

import "unicode"

// pascalCase mirrors the .NET reference's FormatString.Pascalcase: it upper-cases only the
// first rune, leaving the rest of the string untouched (it does not title-case every word) -
// porting this exactly (not a more "helpful" per-word title-case) is what makes
// TopicMethodName/TopicReversedMethodName below match the reference's derived names segment for
// segment.
func pascalCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// removeNonIdentifierChars mirrors FormatString.RemoveNonIdentifierCharacters: keep only
// letters, digits, and underscore.
func removeNonIdentifierChars(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			out = append(out, r)
		}
	}
	return string(out)
}

// removeSpaces mirrors FormatString.RemoveSpaces: drop literal ASCII spaces only.
func removeSpaces(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r != ' ' {
			out = append(out, r)
		}
	}
	return string(out)
}

// ensureStartsWithLetterOrUnderscore mirrors FormatString.EnsureStartsWithLetterOrUnderScore:
// prefix "_" when the first rune is neither a letter nor "_" (so a name derived from e.g. a
// leading digit stays a legal identifier).
func ensureStartsWithLetterOrUnderscore(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if unicode.IsLetter(r[0]) || r[0] == '_' {
		return s
	}
	return "_" + s
}

// formatIdentifierSegment applies the reference's per-segment pipeline
// (RemoveNonIdentifierCharacters -> Pascalcase -> EnsureStartsWithLetterOrUnderScore) that both
// TopicMethodName and TopicReversedMethodName apply to each ":"-delimited topic segment.
func formatIdentifierSegment(s string) string {
	return ensureStartsWithLetterOrUnderscore(pascalCase(removeNonIdentifierChars(s)))
}

// splitTopicSegments splits a topic id on ":", the same delimiter contract-document.md's topic
// ids use (core-concepts.md §2).
func splitTopicSegments(topic string) []string {
	var segments []string
	start := 0
	for i, r := range topic {
		if r == ':' {
			segments = append(segments, topic[start:i])
			start = i + 1
		}
	}
	segments = append(segments, topic[start:])
	return segments
}

// TopicReversedMethodName derives a Go exported method name from topic by the .NET reference's
// TopicReversedMethodName: split on ":", reverse the segments, format each
// (remove-non-identifier -> Pascalcase -> ensure-starts-with-letter), concatenate. E.g.
// "payments:capture" -> "CapturePayments". This is the default per-topic method name on a
// generated service-level client (client.go).
func TopicReversedMethodName(topic string) string {
	segments := splitTopicSegments(topic)
	var out string
	for i := len(segments) - 1; i >= 0; i-- {
		out += formatIdentifierSegment(segments[i])
	}
	return out
}

// TopicMethodName derives a Go exported name from topic by the .NET reference's
// TopicMethodName: split on ":", format each segment (same pipeline as
// TopicReversedMethodName), concatenate in order (not reversed). E.g. "payments:capture" ->
// "PaymentsCapture". Used to name a topic-scoped ("atomic") client's package/type (atomic.go),
// mirroring AtomicClientSdkBuilder's default per-topic client name.
func TopicMethodName(topic string) string {
	segments := splitTopicSegments(topic)
	var out string
	for _, s := range segments {
		out += formatIdentifierSegment(s)
	}
	return out
}

// FormatGoName mirrors the .NET reference's CSharpNameFormatter (EnsureStartsWithLetterOrUnderScore
// -> RemoveSpaces -> Pascalcase) for a schema or property name, producing an exported Go
// identifier. Used for component-schema type names and struct field names; the underlying wire
// name (a JSON property key, or a $ref's schema name) is kept verbatim everywhere else (JSON
// tags, topic strings) per this port's "never re-case a wire string" rule - only the Go
// identifier is formatted.
func FormatGoName(name string) string {
	return pascalCase(removeSpaces(ensureStartsWithLetterOrUnderscore(name)))
}
