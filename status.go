package benzene

// Status is a Benzene result status: a wire-level string, not a closed enum, so
// applications can extend it (docs/specification/wire-contracts.md §3). The values
// below are the framework-defined vocabulary. Their wire strings are
// lowercase-kebab-case and case-sensitive (e.g. "not-found", "validation-error") -
// that casing is the wire contract shared by every Benzene port, so the Go
// identifier names are PascalCase per Go convention while the string values are
// held verbatim to the spec (never emit the identifier name as the wire value).
type Status string

// The framework-defined status vocabulary (wire-contracts.md §3). The string values
// are the case-sensitive lowercase-kebab-case wire contract - do not translate them
// to the Go identifier casing.
const (
	StatusOk                 Status = "ok"
	StatusCreated            Status = "created"
	StatusAccepted           Status = "accepted"
	StatusUpdated            Status = "updated"
	StatusDeleted            Status = "deleted"
	StatusIgnored            Status = "ignored"
	StatusBadRequest         Status = "bad-request"
	StatusValidationError    Status = "validation-error"
	StatusUnauthorized       Status = "unauthorized"
	StatusForbidden          Status = "forbidden"
	StatusNotFound           Status = "not-found"
	StatusConflict           Status = "conflict"
	StatusTooManyRequests    Status = "too-many-requests"
	StatusTimeout            Status = "timeout"
	StatusNotImplemented     Status = "not-implemented"
	StatusServiceUnavailable Status = "service-unavailable"
	StatusUnexpectedError    Status = "unexpected-error"
)

var successStatuses = map[Status]bool{
	StatusOk:       true,
	StatusCreated:  true,
	StatusAccepted: true,
	StatusUpdated:  true,
	StatusDeleted:  true,
	StatusIgnored:  true,
}

var failureStatuses = map[Status]bool{
	StatusBadRequest:         true,
	StatusValidationError:    true,
	StatusUnauthorized:       true,
	StatusForbidden:          true,
	StatusNotFound:           true,
	StatusConflict:           true,
	StatusTooManyRequests:    true,
	StatusTimeout:            true,
	StatusNotImplemented:     true,
	StatusServiceUnavailable: true,
	StatusUnexpectedError:    true,
}

// IsSuccess reports whether status is one of the framework-defined success statuses
// (StatusOk, StatusCreated, StatusAccepted, StatusUpdated, StatusDeleted, StatusIgnored).
// It is false for failure, unknown (application-defined), and empty statuses. This is the
// narrow classifier the per-protocol mapping tables use for their generic-success row; for
// deciding whether an invocation succeeded, prefer IsFailure/Result.IsSuccessful, which do
// not treat an application-defined status as a failure (design-principles.md §"custom
// statuses").
func (s Status) IsSuccess() bool {
	return successStatuses[s]
}

// IsFailure reports whether status is one of the framework-defined failure statuses. It is
// false for success, unknown (application-defined), and empty statuses - an application-defined
// status is not assumed to be a failure, which is what keeps custom statuses flowing through
// the pipeline, envelope, and mesh untouched (design-principles.md).
func (s Status) IsFailure() bool {
	return failureStatuses[s]
}

// IsKnown reports whether status is part of the framework-defined vocabulary (success or
// failure).
func (s Status) IsKnown() bool {
	return s.IsSuccess() || s.IsFailure()
}
