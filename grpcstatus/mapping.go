// Package grpcstatus implements the Benzene<->gRPC status mapping tables of
// daniellepelley/Benzene's docs/specification/wire-contracts.md §4.2.
//
// Codes are returned as the raw numeric gRPC status code
// (https://github.com/grpc/grpc/blob/master/doc/statuscodes.md - a stable, universal part of
// the gRPC spec itself, not tied to any particular language binding), not
// google.golang.org/grpc/codes.Code, so this package stays zero-dependency like httpstatus; a
// gRPC binding wraps the result as codes.Code(grpcstatus.ToGRPC(status)) - codes.Code is
// itself defined as the same underlying numeric type.
package grpcstatus

import benzene "github.com/daniellepelley/benzene-go"

// The numeric gRPC status codes this package's mapping tables reference - named here purely
// for this file's own readability (google.golang.org/grpc/codes.Code has the identical
// values). Codes with no Benzene forward mapping (Unknown, FailedPrecondition, Aborted,
// OutOfRange) are omitted; a gRPC binding may still encounter them on the reverse path (a
// non-Benzene peer), where they fall through FromGRPC's default like any other unmapped code.
const (
	codeOK                = 0
	codeCancelled         = 1
	codeInvalidArgument   = 3
	codeDeadlineExceeded  = 4
	codeNotFound          = 5
	codeAlreadyExists     = 6
	codePermissionDenied  = 7
	codeResourceExhausted = 8
	codeUnimplemented     = 12
	codeInternal          = 13
	codeUnavailable       = 14
	codeDataLoss          = 15
	codeUnauthenticated   = 16
)

// ToGRPC maps a Benzene status to its gRPC status code (wire-contracts.md §4.2, forward).
// An unrecognized or empty status maps to Internal (13), matching the table's own
// "UnexpectedError, unknown, missing -> Internal" row.
func ToGRPC(status benzene.Status) int {
	switch status {
	case benzene.StatusOk, benzene.StatusIgnored, benzene.StatusCreated, benzene.StatusAccepted,
		benzene.StatusUpdated, benzene.StatusDeleted:
		return codeOK
	case benzene.StatusBadRequest, benzene.StatusValidationError:
		return codeInvalidArgument
	case benzene.StatusUnauthorized:
		return codeUnauthenticated
	case benzene.StatusForbidden:
		return codePermissionDenied
	case benzene.StatusNotFound:
		return codeNotFound
	case benzene.StatusConflict:
		return codeAlreadyExists
	case benzene.StatusNotImplemented:
		return codeUnimplemented
	case benzene.StatusServiceUnavailable:
		return codeUnavailable
	case benzene.StatusTooManyRequests:
		return codeResourceExhausted
	case benzene.StatusTimeout:
		return codeDeadlineExceeded
	default: // StatusUnexpectedError, an application-defined status, or empty
		return codeInternal
	}
}

// FromGRPC maps a gRPC status code back to a Benzene status (wire-contracts.md §4.2,
// reverse) - used by a gRPC outbound client reading a response when no benzene-status
// trailer is present (a trailer, when present, wins verbatim - the binding's job, not this
// table's, per the spec text).
func FromGRPC(code int) benzene.Status {
	switch code {
	case codeOK:
		return benzene.StatusOk
	case codeInvalidArgument:
		return benzene.StatusBadRequest
	case codeUnauthenticated:
		return benzene.StatusUnauthorized
	case codePermissionDenied:
		return benzene.StatusForbidden
	case codeNotFound:
		return benzene.StatusNotFound
	case codeAlreadyExists:
		return benzene.StatusConflict
	case codeUnimplemented:
		return benzene.StatusNotImplemented
	case codeUnavailable:
		return benzene.StatusServiceUnavailable
	case codeResourceExhausted:
		return benzene.StatusTooManyRequests
	case codeDeadlineExceeded:
		return benzene.StatusTimeout
	case codeCancelled:
		return benzene.StatusServiceUnavailable
	case codeDataLoss, codeInternal:
		return benzene.StatusUnexpectedError
	default: // Unknown, FailedPrecondition, Aborted, OutOfRange, or any other code
		return benzene.StatusUnexpectedError
	}
}
