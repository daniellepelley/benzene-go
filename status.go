package benzene

// Status is a Benzene result status: a wire-level string, not a closed enum, so
// applications can extend it (docs/specification/wire-contracts.md §3). The values
// below are the framework-defined vocabulary. Their wire strings are
// lowercase-kebab-case and case-sensitive (e.g. "not-found", "validation-error") -
// that casing is the wire contract shared by every Benzene port, so the Go
// identifier names are PascalCase per Go convention while the string values are
// held verbatim to the spec. porting-guide.md §4 calls out emitting the .NET enum's
// PascalCase member name as a .NET-ism that must not leak; the .NET reference itself
// stores kebab-case values (Benzene.Results/BenzeneResultStatus.cs).
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

// IsSuccess reports whether status belongs to the success class. An application-defined
// status not in the framework vocabulary is treated as a failure, matching every
// per-protocol mapping table's "unknown -> generic error" default.
func (s Status) IsSuccess() bool {
	return successStatuses[s]
}
