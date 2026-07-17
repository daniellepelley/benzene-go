package httpstatus

import (
	"strconv"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// These tables are cross-checked against daniellepelley/Benzene's
// docs/specification/conformance/http-status-mapping.json - the authoritative fixture this
// package must agree with. The conformance runner (a later package in this module) proves
// this directly against that JSON file; these are this package's own focused unit tests.

func TestToHTTP_ForwardTable(t *testing.T) {
	tests := []struct {
		status benzene.Status
		want   int
	}{
		{benzene.StatusOk, 200},
		{benzene.StatusIgnored, 200},
		{benzene.StatusCreated, 201},
		{benzene.StatusAccepted, 202},
		{benzene.StatusUpdated, 204},
		{benzene.StatusDeleted, 204},
		{benzene.StatusBadRequest, 400},
		{benzene.StatusUnauthorized, 401},
		{benzene.StatusForbidden, 403},
		{benzene.StatusNotFound, 404},
		{benzene.StatusConflict, 409},
		{benzene.StatusValidationError, 422},
		{benzene.StatusTooManyRequests, 429},
		{benzene.StatusUnexpectedError, 500},
		{benzene.StatusNotImplemented, 501},
		{benzene.StatusServiceUnavailable, 503},
		{benzene.StatusTimeout, 504},
		{benzene.Status("<unknown>"), 500},
		{benzene.Status(""), 500},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := ToHTTP(tt.status); got != tt.want {
				t.Errorf("ToHTTP(%q) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}
}

func TestFromHTTP_ReverseTable(t *testing.T) {
	tests := []struct {
		code int
		want benzene.Status
	}{
		{200, benzene.StatusOk},
		{201, benzene.StatusCreated},
		{202, benzene.StatusAccepted},
		{204, benzene.StatusDeleted},
		{400, benzene.StatusBadRequest},
		{401, benzene.StatusUnauthorized},
		{403, benzene.StatusForbidden},
		{404, benzene.StatusNotFound},
		{408, benzene.StatusTimeout},
		{409, benzene.StatusConflict},
		{422, benzene.StatusValidationError},
		{429, benzene.StatusTooManyRequests},
		{500, benzene.StatusUnexpectedError},
		{501, benzene.StatusNotImplemented},
		{502, benzene.StatusServiceUnavailable},
		{503, benzene.StatusServiceUnavailable},
		{504, benzene.StatusTimeout},
		{418, benzene.StatusUnexpectedError}, // "anything else" fixture case
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			if got := FromHTTP(tt.code); got != tt.want {
				t.Errorf("FromHTTP(%d) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}
