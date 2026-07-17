package benzene

// Result is the outcome of a single handler invocation (docs/specification/core-concepts.md
// §5 in the main Benzene repo). Results are values, not exceptions - a transport binding
// translates a non-success Status into that transport's native failure signal.
type Result[T any] struct {
	Status Status
	// Payload is present on success (and optionally on failure). It's a pointer so
	// "absent" is representable without colliding with T's own zero value.
	Payload *T
	// Errors holds zero or more human-readable error messages, populated on failure.
	Errors []string
}

// IsSuccessful reports whether Status belongs to the success class.
func (r Result[T]) IsSuccessful() bool {
	return r.Status.IsSuccess()
}

// ResultInfo is the type-erased view of a Result[T], implemented by every instantiation.
// The registry stores handlers behind a non-generic dispatch signature (Go generics can't
// hold heterogeneous Result[T] instantiations in one collection), so transport bindings and
// the pipeline recover status/errors/payload through this interface instead of the concrete
// generic type, which they can't name without knowing T.
type ResultInfo interface {
	ResultStatus() Status
	ResultErrors() []string
	// ResultPayload returns the payload as `any` (nil if absent) for generic serialization.
	ResultPayload() any
}

func (r Result[T]) ResultStatus() Status   { return r.Status }
func (r Result[T]) ResultErrors() []string { return r.Errors }
func (r Result[T]) ResultPayload() any {
	if r.Payload == nil {
		return nil
	}
	return *r.Payload
}

var _ ResultInfo = Result[struct{}]{}

func success[T any](status Status, payload T) Result[T] {
	return Result[T]{Status: status, Payload: &payload}
}

// Ok returns a successful Result with StatusOk.
func Ok[T any](payload T) Result[T] { return success(StatusOk, payload) }

// CreatedResult returns a successful Result with StatusCreated.
func CreatedResult[T any](payload T) Result[T] { return success(StatusCreated, payload) }

// Accepted returns a successful Result with StatusAccepted.
func Accepted[T any](payload T) Result[T] { return success(StatusAccepted, payload) }

// Updated returns a successful Result with StatusUpdated.
func Updated[T any](payload T) Result[T] { return success(StatusUpdated, payload) }

// Deleted returns a successful Result with StatusDeleted.
func Deleted[T any](payload T) Result[T] { return success(StatusDeleted, payload) }

// Ignored returns a successful Result with StatusIgnored - handled deliberately, not an error.
func Ignored[T any](payload T) Result[T] { return success(StatusIgnored, payload) }

// Fail returns a failed Result with the given status and error messages. Panics if status
// is in the success class, since that would produce a self-contradictory Result (mirrors the
// C# BenzeneResult's guard against the same mistake).
func Fail[T any](status Status, errors ...string) Result[T] {
	if status.IsSuccess() {
		panic("benzene: Fail called with a success-class status " + string(status))
	}
	return Result[T]{Status: status, Errors: errors}
}

// BadRequest returns a failed Result with StatusBadRequest.
func BadRequest[T any](errors ...string) Result[T] { return Fail[T](StatusBadRequest, errors...) }

// ValidationError returns a failed Result with StatusValidationError.
func ValidationError[T any](errors ...string) Result[T] {
	return Fail[T](StatusValidationError, errors...)
}

// Unauthorized returns a failed Result with StatusUnauthorized.
func Unauthorized[T any](errors ...string) Result[T] { return Fail[T](StatusUnauthorized, errors...) }

// Forbidden returns a failed Result with StatusForbidden.
func Forbidden[T any](errors ...string) Result[T] { return Fail[T](StatusForbidden, errors...) }

// NotFound returns a failed Result with StatusNotFound.
func NotFound[T any](errors ...string) Result[T] { return Fail[T](StatusNotFound, errors...) }

// Conflict returns a failed Result with StatusConflict.
func Conflict[T any](errors ...string) Result[T] { return Fail[T](StatusConflict, errors...) }

// TooManyRequests returns a failed Result with StatusTooManyRequests - throttled/rate
// limited; transient, safe to retry after backing off.
func TooManyRequests[T any](errors ...string) Result[T] {
	return Fail[T](StatusTooManyRequests, errors...)
}

// Timeout returns a failed Result with StatusTimeout - a downstream deadline elapsed;
// transient, but whether the operation was applied is unknown, so blind retries are only
// safe for idempotent operations (unlike StatusServiceUnavailable, RetryDecorator does not
// retry this status by default).
func Timeout[T any](errors ...string) Result[T] { return Fail[T](StatusTimeout, errors...) }

// NotImplemented returns a failed Result with StatusNotImplemented.
func NotImplemented[T any](errors ...string) Result[T] {
	return Fail[T](StatusNotImplemented, errors...)
}

// ServiceUnavailable returns a failed Result with StatusServiceUnavailable - also the mapping
// used for uncaught handler panics and client-side send failures.
func ServiceUnavailable[T any](errors ...string) Result[T] {
	return Fail[T](StatusServiceUnavailable, errors...)
}

// UnexpectedError returns a failed Result with StatusUnexpectedError.
func UnexpectedError[T any](errors ...string) Result[T] {
	return Fail[T](StatusUnexpectedError, errors...)
}
