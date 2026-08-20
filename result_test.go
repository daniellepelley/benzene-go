package benzene

import (
	"reflect"
	"testing"
)

type greeting struct {
	Message string
}

func TestOk(t *testing.T) {
	r := Ok(greeting{Message: "hi"})

	if r.Status != StatusOk {
		t.Errorf("Status = %q, want %q", r.Status, StatusOk)
	}
	if !r.IsSuccessful() {
		t.Error("IsSuccessful() = false, want true")
	}
	if r.Payload == nil || r.Payload.Message != "hi" {
		t.Errorf("Payload = %+v, want {Message: hi}", r.Payload)
	}
	if len(r.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", r.Errors)
	}
}

func TestResult_ApplicationDefinedStatusIsSuccessful(t *testing.T) {
	// An application-defined status is not a framework failure, so IsSuccessful is true and
	// the payload is carried - the extensibility promise (design-principles.md), matching the
	// .NET reference where IsSuccessful defaults to !IsFailure(status).
	payload := greeting{Message: "partial"}
	r := Result[greeting]{Status: Status("partial-success"), Payload: &payload}
	if !r.IsSuccessful() {
		t.Errorf("IsSuccessful() = false for an application-defined status, want true")
	}
	// A framework failure status, by contrast, is not successful.
	f := Result[greeting]{Status: StatusServiceUnavailable}
	if f.IsSuccessful() {
		t.Errorf("IsSuccessful() = true for %q, want false", StatusServiceUnavailable)
	}
	// An application-defined status raised via Fail (the errors-based failure constructor) is
	// NOT successful even though it is not a framework failure - it carries errors, so it is a
	// failure by construction, mirroring .NET's errors-based Set.
	if Fail[greeting](Status("partial-failure"), "boom").IsSuccessful() {
		t.Error("IsSuccessful() = true for a Fail() with an application-defined status, want false")
	}
}

func TestSetResult_ExplicitSuccessDecoupledFromStatus(t *testing.T) {
	// The health-check shape: service-unavailable (503 to probes) but explicitly successful so
	// the report body renders. Mirrors .NET BenzeneResult.Set(status, payload, isSuccessful).
	payload := greeting{Message: "report"}
	r := SetResult(StatusServiceUnavailable, payload, true)
	if !r.IsSuccessful() {
		t.Error("IsSuccessful() = false, want true (explicit flag overrides the failure status)")
	}
	if r.Status != StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", r.Status, StatusServiceUnavailable)
	}
	if r.Payload == nil || r.Payload.Message != "report" {
		t.Errorf("Payload = %+v, want the report payload", r.Payload)
	}
	if r.ResultIsSuccessful() != r.IsSuccessful() {
		t.Error("ResultIsSuccessful() must mirror IsSuccessful() on the type-erased path")
	}
	// The inverse: explicitly not-successful despite a success status.
	if SetResult(StatusOk, payload, false).IsSuccessful() {
		t.Error("IsSuccessful() = true, want false when explicitly set false")
	}
}

func TestSuccessConstructors(t *testing.T) {
	tests := []struct {
		name       string
		result     Result[greeting]
		wantStatus Status
	}{
		{"Ok", Ok(greeting{}), StatusOk},
		{"Created", Created(greeting{}), StatusCreated},
		{"Accepted", Accepted(greeting{}), StatusAccepted},
		{"Updated", Updated(greeting{}), StatusUpdated},
		{"Deleted", Deleted(greeting{}), StatusDeleted},
		{"Ignored", Ignored(greeting{}), StatusIgnored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", tt.result.Status, tt.wantStatus)
			}
			if !tt.result.IsSuccessful() {
				t.Errorf("%s result should be successful", tt.name)
			}
		})
	}
}

func TestFailureConstructors(t *testing.T) {
	tests := []struct {
		name       string
		result     Result[greeting]
		wantStatus Status
	}{
		{"BadRequest", BadRequest[greeting]("bad"), StatusBadRequest},
		{"ValidationError", ValidationError[greeting]("invalid"), StatusValidationError},
		{"Unauthorized", Unauthorized[greeting](), StatusUnauthorized},
		{"Forbidden", Forbidden[greeting](), StatusForbidden},
		{"NotFound", NotFound[greeting]("missing"), StatusNotFound},
		{"Conflict", Conflict[greeting](), StatusConflict},
		{"TooManyRequests", TooManyRequests[greeting]("slow down"), StatusTooManyRequests},
		{"Timeout", Timeout[greeting]("deadline exceeded"), StatusTimeout},
		{"NotImplemented", NotImplemented[greeting](), StatusNotImplemented},
		{"ServiceUnavailable", ServiceUnavailable[greeting]("down"), StatusServiceUnavailable},
		{"UnexpectedError", UnexpectedError[greeting]("boom"), StatusUnexpectedError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", tt.result.Status, tt.wantStatus)
			}
			if tt.result.IsSuccessful() {
				t.Errorf("%s result should not be successful", tt.name)
			}
			if tt.result.Payload != nil {
				t.Errorf("Payload = %+v, want nil for a failure", tt.result.Payload)
			}
		})
	}
}

func TestFail_ErrorsPreserved(t *testing.T) {
	r := Fail[greeting](StatusNotFound, "no handler", "for topic order:create")
	want := []Error{{Message: "no handler"}, {Message: "for topic order:create"}}
	if !reflect.DeepEqual(r.Errors, want) {
		t.Errorf("Errors = %v, want %v", r.Errors, want)
	}
}

// The plain-string path must stay exactly as cheap as it was: a message-only failure carries a
// Message and nothing else, and never invents a Field or a Code.
func TestFail_WrapsMessagesAsMessageOnlyErrors(t *testing.T) {
	r := Fail[greeting](StatusNotFound, "gone")
	if len(r.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(r.Errors))
	}
	if r.Errors[0].Field != "" || r.Errors[0].Code != "" {
		t.Errorf("Errors[0] = %+v, want Field and Code empty", r.Errors[0])
	}
}

func TestFail_NoMessagesLeavesErrorsNil(t *testing.T) {
	if r := Fail[greeting](StatusNotFound); r.Errors != nil {
		t.Errorf("Errors = %v, want nil when no messages are given", r.Errors)
	}
}

func TestFailWith_PreservesFieldAndCode(t *testing.T) {
	r := FailWith[greeting](StatusValidationError,
		Error{Message: "Name must not be empty", Field: "Name", Code: "NotEmptyValidator"})

	if len(r.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(r.Errors))
	}
	got := r.Errors[0]
	if got.Message != "Name must not be empty" || got.Field != "Name" || got.Code != "NotEmptyValidator" {
		t.Errorf("Errors[0] = %+v, want message/field/code carried verbatim", got)
	}
	if r.IsSuccessful() {
		t.Error("IsSuccessful() = true, want false for a structured failure")
	}
}

func TestFailWith_PanicsOnSuccessStatus(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("FailWith with a success-class status should panic")
		}
	}()
	_ = FailWith[greeting](StatusOk, Error{Message: "contradiction"})
}

func TestValidationErrorWith_UsesTheValidationStatus(t *testing.T) {
	r := ValidationErrorWith[greeting](Error{Message: "too short", Field: "Name", Code: "MinLength"})
	if r.Status != StatusValidationError {
		t.Errorf("Status = %q, want %q", r.Status, StatusValidationError)
	}
	if r.Errors[0].Code != "MinLength" {
		t.Errorf("Code = %q, want MinLength", r.Errors[0].Code)
	}
}

// ResultErrors is the type-erased view every existing binding uses. Structured errors must not
// change what it returns, or adding a Field somewhere would silently alter unrelated bindings.
func TestResultErrors_FlattensToMessages(t *testing.T) {
	r := FailWith[greeting](StatusValidationError,
		Error{Message: "first", Field: "A", Code: "X"},
		Error{Message: "second"})

	want := []string{"first", "second"}
	if got := r.ResultErrors(); !reflect.DeepEqual(got, want) {
		t.Errorf("ResultErrors() = %v, want %v", got, want)
	}
}

func TestResultErrors_NilWhenThereAreNone(t *testing.T) {
	if got := Ok(greeting{}).ResultErrors(); got != nil {
		t.Errorf("ResultErrors() = %v, want nil", got)
	}
}

func TestProblemsOf_UsesTheStructuredViewWhenAvailable(t *testing.T) {
	var info ResultInfo = FailWith[greeting](StatusValidationError,
		Error{Message: "bad", Field: "Name", Code: "NotEmpty"})

	got := ProblemsOf(info)
	if len(got) != 1 || got[0].Field != "Name" || got[0].Code != "NotEmpty" {
		t.Errorf("ProblemsOf() = %+v, want the field and code preserved", got)
	}
}

// messagesOnlyResult is a ResultInfo that predates ProblemInfo - the case ProblemsOf's fallback
// exists for. Written out rather than mocked so the fallback is exercised by a type that genuinely
// does not implement the optional interface.
type messagesOnlyResult struct{}

func (messagesOnlyResult) ResultStatus() Status   { return StatusValidationError }
func (messagesOnlyResult) ResultErrors() []string { return []string{"legacy"} }
func (messagesOnlyResult) ResultPayload() any     { return nil }

func TestProblemsOf_FallsBackToMessagesWithoutProblemInfo(t *testing.T) {
	if _, ok := any(messagesOnlyResult{}).(ProblemInfo); ok {
		t.Fatal("messagesOnlyResult must not implement ProblemInfo, or this test proves nothing")
	}

	got := ProblemsOf(messagesOnlyResult{})
	want := []Error{{Message: "legacy"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProblemsOf() = %+v, want %+v", got, want)
	}
}

func TestFail_PanicsOnSuccessStatus(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Fail with a success-class status should panic")
		}
	}()
	Fail[greeting](StatusOk)
}

func TestResultInfo_ExposesTypeErasedView(t *testing.T) {
	var info ResultInfo = Ok(greeting{Message: "hi"})

	if info.ResultStatus() != StatusOk {
		t.Errorf("ResultStatus() = %q, want %q", info.ResultStatus(), StatusOk)
	}
	payload, ok := info.ResultPayload().(greeting)
	if !ok || payload.Message != "hi" {
		t.Errorf("ResultPayload() = %v, want greeting{Message: hi}", info.ResultPayload())
	}
	if len(info.ResultErrors()) != 0 {
		t.Errorf("ResultErrors() = %v, want empty", info.ResultErrors())
	}
}

func TestResultInfo_NilPayloadOnFailure(t *testing.T) {
	var info ResultInfo = NotFound[greeting]("missing")
	if info.ResultPayload() != nil {
		t.Errorf("ResultPayload() = %v, want nil", info.ResultPayload())
	}
}
