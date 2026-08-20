package wire

import "testing"

// The §3.1 registry lookups and ErrorPayload.Problems had no direct tests in this package. Both were
// exercised from elsewhere - the conformance runner reads the registry, and the HTTP client's
// round-trip test reaches Problems - so both were covered in practice and 0.0% here, which is the
// gap this file closes. A dependency-free package's own suite should stand on its own.

func TestProblemType_ReturnsTheRegistryURI(t *testing.T) {
	if got, want := ProblemType("validation-error"), ProblemBase+"validation-error"; got != want {
		t.Errorf("ProblemType(validation-error) = %q, want %q", got, want)
	}
}

func TestProblemType_IsEmptyForAnApplicationDefinedStatus(t *testing.T) {
	// Empty, not a fabricated benzene.app URI: §3.1 says such a failure carries the application's
	// own URI or omits the member, and inventing one under our namespace would be speaking for them.
	if got := ProblemType("insufficient-funds"); got != "" {
		t.Errorf("ProblemType(insufficient-funds) = %q, want empty", got)
	}
}

func TestProblemTitle_IsEmptyForAnApplicationDefinedStatus(t *testing.T) {
	if got := ProblemTitle("insufficient-funds"); got != "" {
		t.Errorf("ProblemTitle(insufficient-funds) = %q, want empty", got)
	}
}

func TestProblemTitle_ReturnsSomethingForEveryRegistryStatus(t *testing.T) {
	// The wording is never asserted (identity is fixed, prose is free), but a row with no title at
	// all is a row that has fallen out of the table.
	for status := range problemRegistry {
		if ProblemTitle(status) == "" {
			t.Errorf("ProblemTitle(%q) is empty, want a registry title", status)
		}
	}
}

func TestProblemHTTPStatus_ReturnsTheRegistryCode(t *testing.T) {
	for status, want := range map[string]int{
		"bad-request":         400,
		"not-found":           404,
		"validation-error":    422,
		"service-unavailable": 503,
		"timeout":             504,
	} {
		if got := ProblemHTTPStatus(status); got != want {
			t.Errorf("ProblemHTTPStatus(%q) = %d, want %d", status, got, want)
		}
	}
}

func TestProblemHTTPStatus_FallsTo500ForAnApplicationDefinedStatus(t *testing.T) {
	// §4.1's unknown-status row. Unlike type and title, there IS a right answer here: a transport
	// must send some code, and the generic-error one is it.
	if got := ProblemHTTPStatus("insufficient-funds"); got != 500 {
		t.Errorf("ProblemHTTPStatus(insufficient-funds) = %d, want 500", got)
	}
}

func TestProblems_PrefersTheErrorsArray(t *testing.T) {
	payload := ErrorPayload{
		Detail: "joined, prose",
		Errors: []ProblemError{
			{Message: "sku is required", Field: "sku", Code: "required"},
			{Message: "quantity must be positive", Field: "quantity", Code: "positive"},
		},
	}

	got := payload.Problems()

	if len(got) != 2 {
		t.Fatalf("len(Problems()) = %d, want 2", len(got))
	}
	if got[0].Field != "sku" || got[0].Code != "required" {
		t.Errorf("Problems()[0] = %+v, want the field and code carried through", got[0])
	}
	if got[1].Field != "quantity" {
		t.Errorf("Problems()[1] = %+v, want the second error in order", got[1])
	}
}

func TestProblems_FallsBackToDetailAsOneOpaqueError(t *testing.T) {
	// One error, not two. Splitting detail on ", " was withdrawn by the RFC 9457 revision precisely
	// because error messages contain commas, and this message does.
	got := ErrorPayload{Detail: "one, two"}.Problems()

	if len(got) != 1 {
		t.Fatalf("Problems() = %+v, want exactly one opaque error", got)
	}
	if got[0].Message != "one, two" {
		t.Errorf("Problems()[0].Message = %q, want the detail unsplit", got[0].Message)
	}
}

func TestProblems_IsNilWhenTheDocumentSaysNothing(t *testing.T) {
	if got := (ErrorPayload{BenzeneStatus: "not-found"}).Problems(); got != nil {
		t.Errorf("Problems() = %+v, want nil", got)
	}
}

func TestProblems_SkipsAnEmptyMessage(t *testing.T) {
	// A peer sending {"errors":[{"field":"sku"}]} has said nothing readable, and an error whose
	// message is blank is worse than no entry - it renders as an empty bullet.
	got := ErrorPayload{Errors: []ProblemError{{Field: "sku"}, {Message: "real"}}}.Problems()

	if len(got) != 1 || got[0].Message != "real" {
		t.Errorf("Problems() = %+v, want only the error that carries a message", got)
	}
}

func TestMessages_AndProblems_AgreeOnWhichSourceWins(t *testing.T) {
	// The two views of one document must not disagree about precedence, or a caller's choice of
	// accessor would silently change which errors it sees.
	payload := ErrorPayload{Detail: "detail wins only when errors is absent", Errors: []ProblemError{{Message: "from errors"}}}

	messages, problems := payload.Messages(), payload.Problems()

	if len(messages) != len(problems) {
		t.Fatalf("Messages() has %d and Problems() has %d - they must agree", len(messages), len(problems))
	}
	if messages[0] != problems[0].Message {
		t.Errorf("Messages()[0] = %q but Problems()[0].Message = %q", messages[0], problems[0].Message)
	}
}

func TestUnmarshalErrorPayload_StillFailsWhenASecondMemberIsAlsoMistyped(t *testing.T) {
	// The legacy-`status` tolerance drops exactly one member and re-parses the rest. If the rest is
	// also wrong - here `detail` arriving as a number - there is nothing left to be lenient about,
	// and the error must surface rather than an empty payload being returned as if it had worked.
	//
	// This is the relaxed parse's own failure branch, which had no test: the happy legacy path was
	// covered, the second-failure path was not.
	_, err := UnmarshalErrorPayload([]byte(`{"status":"not-found","detail":123}`))

	if err == nil {
		t.Fatal("UnmarshalErrorPayload() error = nil, want an error when the relaxed parse also fails")
	}
}
