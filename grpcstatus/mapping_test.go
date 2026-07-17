package grpcstatus

import (
	"strconv"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// These tables are cross-checked against daniellepelley/Benzene's
// docs/specification/conformance/grpc-status-mapping.json - the authoritative fixture this
// package must agree with. The conformance runner (a later package in this module) proves
// this directly against that JSON file; these are this package's own focused unit tests.

func TestToGRPC_ForwardTable(t *testing.T) {
	tests := []struct {
		status benzene.Status
		want   int
	}{
		{benzene.StatusOk, codeOK},
		{benzene.StatusIgnored, codeOK},
		{benzene.StatusCreated, codeOK},
		{benzene.StatusAccepted, codeOK},
		{benzene.StatusUpdated, codeOK},
		{benzene.StatusDeleted, codeOK},
		{benzene.StatusBadRequest, codeInvalidArgument},
		{benzene.StatusValidationError, codeInvalidArgument},
		{benzene.StatusUnauthorized, codeUnauthenticated},
		{benzene.StatusForbidden, codePermissionDenied},
		{benzene.StatusNotFound, codeNotFound},
		{benzene.StatusConflict, codeAlreadyExists},
		{benzene.StatusNotImplemented, codeUnimplemented},
		{benzene.StatusServiceUnavailable, codeUnavailable},
		{benzene.StatusTooManyRequests, codeResourceExhausted},
		{benzene.StatusTimeout, codeDeadlineExceeded},
		{benzene.StatusUnexpectedError, codeInternal},
		{benzene.Status("<unknown>"), codeInternal},
		{benzene.Status(""), codeInternal},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := ToGRPC(tt.status); got != tt.want {
				t.Errorf("ToGRPC(%q) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}
}

func TestFromGRPC_ReverseTable(t *testing.T) {
	tests := []struct {
		code int
		want benzene.Status
	}{
		{codeOK, benzene.StatusOk},
		{codeInvalidArgument, benzene.StatusBadRequest},
		{codeUnauthenticated, benzene.StatusUnauthorized},
		{codePermissionDenied, benzene.StatusForbidden},
		{codeNotFound, benzene.StatusNotFound},
		{codeAlreadyExists, benzene.StatusConflict},
		{codeUnimplemented, benzene.StatusNotImplemented},
		{codeUnavailable, benzene.StatusServiceUnavailable},
		{codeResourceExhausted, benzene.StatusTooManyRequests},
		{codeDeadlineExceeded, benzene.StatusTimeout},
		{codeCancelled, benzene.StatusServiceUnavailable},
		{codeDataLoss, benzene.StatusUnexpectedError},
		{codeInternal, benzene.StatusUnexpectedError},
		{2, benzene.StatusUnexpectedError},  // Unknown - not in the fixture's reverse table
		{9, benzene.StatusUnexpectedError},  // FailedPrecondition - ditto
		{99, benzene.StatusUnexpectedError}, // not a real gRPC code at all
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			if got := FromGRPC(tt.code); got != tt.want {
				t.Errorf("FromGRPC(%d) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}
