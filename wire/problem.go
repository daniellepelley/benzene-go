package wire

import "encoding/json"

// ProblemBase is the namespace every framework-defined problem type URI lives under
// (wire-contracts.md §3.1). These URIs are opaque identifiers, not live pages: a reader compares
// them by string equality and never dereferences one.
const ProblemBase = "https://benzene.app/problems/"

// problemRow is one row of the §3.1 registry: the type slug, the fixed human title, and the HTTP
// status an HTTP binding must send (and echo in the document's own status member, §4.1).
type problemRow struct {
	slug   string
	title  string
	status int
}

// The registry is keyed by the §3 status vocabulary, so it introduces no second taxonomy. Success
// statuses have no row - problem documents exist only on failure.
var problemRegistry = map[string]problemRow{
	"bad-request":         {"bad-request", "Bad request", 400},
	"unauthorized":        {"unauthorized", "Unauthorized", 401},
	"forbidden":           {"forbidden", "Forbidden", 403},
	"not-found":           {"not-found", "Not found", 404},
	"conflict":            {"conflict", "Conflict", 409},
	"validation-error":    {"validation-error", "Validation failed", 422},
	"too-many-requests":   {"too-many-requests", "Too many requests", 429},
	"unexpected-error":    {"unexpected-error", "Unexpected error", 500},
	"not-implemented":     {"not-implemented", "Not implemented", 501},
	"service-unavailable": {"service-unavailable", "Service unavailable", 503},
	"timeout":             {"timeout", "Timeout", 504},
}

// ProblemType returns the registry type URI for status, or "" for an application-defined status.
// Empty is the correct answer for a status the registry does not know: §3.1 says such a failure
// carries the application's own URI or omits the member, and the framework has no business
// inventing one under the benzene.app namespace on the application's behalf.
func ProblemType(status string) string {
	row, ok := problemRegistry[status]
	if !ok {
		return ""
	}
	return ProblemBase + row.slug
}

// ProblemTitle returns the registry title for status, or "" for an application-defined status.
// Titles are fixed per type and are never asserted by the conformance fixtures - wording is free,
// identity is not.
func ProblemTitle(status string) string {
	return problemRegistry[status].title
}

// ProblemHTTPStatus returns the HTTP code for status (§4.1). An unknown or application-defined
// status falls to 500, the same unknown-status row the rest of the document uses.
func ProblemHTTPStatus(status string) int {
	row, ok := problemRegistry[status]
	if !ok {
		return 500
	}
	return row.status
}

// WithHTTPStatus re-serializes an already-encoded problem document with its `status` member set
// to code - the §4.1 rule that an HTTP failure's document MUST carry the code actually being
// sent. The transport-neutral document omits the member (§1.3) precisely because most transports
// have no HTTP response for it to equal, so filling it in is each HTTP binding's job; this is the
// one implementation all of them share, so they cannot drift apart on it.
//
// It reports false, leaving body untouched, when body is not a JSON object - an empty body, or a
// peer sending something the binding should pass through rather than guess at.
func WithHTTPStatus(body string, code int) (string, bool) {
	var problem map[string]any
	if err := json.Unmarshal([]byte(body), &problem); err != nil || problem == nil {
		return body, false
	}
	problem["status"] = code
	encoded, err := json.Marshal(problem)
	if err != nil {
		return body, false
	}
	return string(encoded), true
}
