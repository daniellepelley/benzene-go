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
