package benzene

// Status is a Benzene result status: a wire-level string, not a closed enum, so
// applications can extend it (docs/specification/wire-contracts.md §3). The values
// below are the framework-defined vocabulary and are PascalCase and case-sensitive
// on the wire - that casing is a wire contract, not a Go naming convention, so it is
// preserved verbatim rather than translated to Go's usual camelCase/PascalCase-for-
// exported-identifiers rules.
type Status string

// The framework-defined status vocabulary (wire-contracts.md §3).
const (
	StatusOk                 Status = "Ok"
	StatusCreated            Status = "Created"
	StatusAccepted           Status = "Accepted"
	StatusUpdated            Status = "Updated"
	StatusDeleted            Status = "Deleted"
	StatusIgnored            Status = "Ignored"
	StatusBadRequest         Status = "BadRequest"
	StatusValidationError    Status = "ValidationError"
	StatusUnauthorized       Status = "Unauthorized"
	StatusForbidden          Status = "Forbidden"
	StatusNotFound           Status = "NotFound"
	StatusConflict           Status = "Conflict"
	StatusNotImplemented     Status = "NotImplemented"
	StatusServiceUnavailable Status = "ServiceUnavailable"
	StatusUnexpectedError    Status = "UnexpectedError"
)

var successStatuses = map[Status]bool{
	StatusOk:       true,
	StatusCreated:  true,
	StatusAccepted: true,
	StatusUpdated:  true,
	StatusDeleted:  true,
	StatusIgnored:  true,
}

// IsSuccess reports whether status belongs to the success class. An application-defined
// status not in the framework vocabulary is treated as a failure, matching every
// per-protocol mapping table's "unknown -> generic error" default.
func (s Status) IsSuccess() bool {
	return successStatuses[s]
}
