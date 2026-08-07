package benzene

import "testing"

func TestStatus_IsSuccess(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusOk, true},
		{StatusCreated, true},
		{StatusAccepted, true},
		{StatusUpdated, true},
		{StatusDeleted, true},
		{StatusIgnored, true},
		{StatusBadRequest, false},
		{StatusValidationError, false},
		{StatusUnauthorized, false},
		{StatusForbidden, false},
		{StatusNotFound, false},
		{StatusConflict, false},
		{StatusTooManyRequests, false},
		{StatusTimeout, false},
		{StatusNotImplemented, false},
		{StatusServiceUnavailable, false},
		{StatusUnexpectedError, false},
		{Status("SomeApplicationDefinedStatus"), false},
		{Status(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsSuccess(); got != tt.want {
				t.Errorf("Status(%q).IsSuccess() = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestStatus_IsFailure(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusOk, false},
		{StatusCreated, false},
		{StatusAccepted, false},
		{StatusUpdated, false},
		{StatusDeleted, false},
		{StatusIgnored, false},
		{StatusBadRequest, true},
		{StatusValidationError, true},
		{StatusUnauthorized, true},
		{StatusForbidden, true},
		{StatusNotFound, true},
		{StatusConflict, true},
		{StatusTooManyRequests, true},
		{StatusTimeout, true},
		{StatusNotImplemented, true},
		{StatusServiceUnavailable, true},
		{StatusUnexpectedError, true},
		// An application-defined status is NOT assumed to be a failure - this is what keeps
		// custom statuses flowing through the pipeline and envelope untouched.
		{Status("SomeApplicationDefinedStatus"), false},
		{Status(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsFailure(); got != tt.want {
				t.Errorf("Status(%q).IsFailure() = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestStatus_IsKnown(t *testing.T) {
	// Every framework status is known; an application-defined and the empty status are not.
	for _, s := range []Status{
		StatusOk, StatusCreated, StatusAccepted, StatusUpdated, StatusDeleted, StatusIgnored,
		StatusBadRequest, StatusValidationError, StatusUnauthorized, StatusForbidden,
		StatusNotFound, StatusConflict, StatusTooManyRequests, StatusTimeout,
		StatusNotImplemented, StatusServiceUnavailable, StatusUnexpectedError,
	} {
		if !s.IsKnown() {
			t.Errorf("Status(%q).IsKnown() = false, want true", s)
		}
	}
	for _, s := range []Status{Status("custom"), Status("")} {
		if s.IsKnown() {
			t.Errorf("Status(%q).IsKnown() = true, want false", s)
		}
	}
}

// TestStatus_WireValuesAreKebabCase pins the framework vocabulary's wire strings to the
// case-sensitive lowercase-kebab-case contract of wire-contracts.md §3 - a regression guard
// against the PascalCase leak that porting-guide.md §4 forbids.
func TestStatus_WireValuesAreKebabCase(t *testing.T) {
	want := map[Status]string{
		StatusOk: "ok", StatusCreated: "created", StatusAccepted: "accepted",
		StatusUpdated: "updated", StatusDeleted: "deleted", StatusIgnored: "ignored",
		StatusBadRequest: "bad-request", StatusValidationError: "validation-error",
		StatusUnauthorized: "unauthorized", StatusForbidden: "forbidden",
		StatusNotFound: "not-found", StatusConflict: "conflict",
		StatusTooManyRequests: "too-many-requests", StatusTimeout: "timeout",
		StatusNotImplemented: "not-implemented", StatusServiceUnavailable: "service-unavailable",
		StatusUnexpectedError: "unexpected-error",
	}
	for status, wire := range want {
		if string(status) != wire {
			t.Errorf("status wire value = %q, want %q", string(status), wire)
		}
	}
}
