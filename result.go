package benzene

import "github.com/daniellepelley/benzene-go/wire"

// Error is one structured error on a failed Result (wire-contracts.md §1.3): Message is the only
// required member, and Field and Code are the producer's own property path and machine-readable rule
// identifier, emitted verbatim - the framework never normalizes or rewords them.
//
// A schema validator that knows a message, the field it came from and the rule that rejected it can
// say all three, and the problem document carries them through to the caller. Without that a
// consumer gets prose it has to parse, which is the difference between an error a UI can attach to
// an input and an error it can only print.
//
// It is an alias, not a copy, of wire.ProblemError: the value a handler builds IS the value that
// reaches the wire, so there is no second shape to keep in step and no conversion that can quietly
// drop a member. benzene.Error is simply the name to use from application code, where importing a
// package called "wire" to describe a validation failure would read oddly.
type Error = wire.ProblemError

// Problem is an application-authored problem document (wire-contracts.md §1.3) - the escape hatch
// for a service that owns its own problem vocabulary and wants its own `type` URI on the wire
// instead of the registry URI Benzene would derive from the status. Build one and hand it to
// ProblemResult.
//
// Also an alias of the wire type, for the same reason as Error. Note that Status (the HTTP status
// number) is not something an application authors: an HTTP binding sets it to the code it is
// actually sending, and it is absent on every other transport, so leave it nil.
type Problem = wire.ErrorPayload

// errorsFromMessages wraps plain messages as structured errors carrying Message only. This is what
// keeps the common case free of ceremony: Fail(status, "boom") stays two words, and the structure
// is there for the producers that have it.
func errorsFromMessages(messages []string) []Error {
	if len(messages) == 0 {
		return nil
	}
	errs := make([]Error, 0, len(messages))
	for _, m := range messages {
		errs = append(errs, Error{Message: m})
	}
	return errs
}

// Result is the outcome of a single handler invocation (docs/specification/core-concepts.md
// §5 in the main Benzene repo). Results are values, not exceptions - a transport binding
// translates a non-success Status into that transport's native failure signal.
type Result[T any] struct {
	Status Status
	// Payload is present on success (and optionally on failure). It's a pointer so
	// "absent" is representable without colliding with T's own zero value.
	Payload *T
	// Errors holds zero or more structured errors, populated on failure. The zero-ceremony
	// constructors (Fail, ValidationError, ...) take plain strings and fill in Message only;
	// reach for FailWith/ValidationErrorWith when the producer knows a Field or a Code.
	Errors []Error
	// successful, when non-nil, overrides the status-derived success classification (see
	// SetResult and IsSuccessful). Unexported so the only way to set it is the deliberate
	// SetResult constructor; a plain struct literal leaves it nil and derives from the status.
	successful *bool
	// problem, when non-nil, is an application-authored problem document that the wire edge emits
	// verbatim instead of deriving one from the status - otherwise an application's own `type` URI
	// would be overwritten by the registry URI on the way out. Set only by ProblemResult.
	problem *Problem
}

// IsSuccessful reports whether this result should be treated as a success. Unless an explicit
// flag was set via SetResult, it is derived from the status class as "not a failure"
// (core-concepts.md §5), so a framework success status and an application-defined status both
// count as successful and carry their payload, while only a framework failure status does not -
// the extensibility promise that custom statuses flow through untouched (design-principles.md).
func (r Result[T]) IsSuccessful() bool {
	if r.successful != nil {
		return *r.successful
	}
	return !r.Status.IsFailure()
}

// ResultIsSuccessful exposes IsSuccessful on the type-erased ResultInfo path. A transport
// binding renders the payload vs an error body from the ResultInfo it holds, and this lets an
// explicit success flag (SetResult) survive type erasure; a binding checks for it via the
// optional interface { ResultIsSuccessful() bool } and falls back to the status otherwise.
func (r Result[T]) ResultIsSuccessful() bool { return r.IsSuccessful() }

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

func (r Result[T]) ResultStatus() Status { return r.Status }

// ResultErrors returns the error messages, flattening the structured errors. The type-erased
// interface deliberately keeps returning []string: every binding that only ever wanted messages is
// unaffected by structured errors existing, and the ones that want more ask via ProblemInfo.
func (r Result[T]) ResultErrors() []string {
	if len(r.Errors) == 0 {
		return nil
	}
	messages := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		messages = append(messages, e.Message)
	}
	return messages
}

// ResultProblems exposes the structured errors on the type-erased path, so a binding building a
// problem document (wire-contracts.md §1.3) can carry field and code through instead of flattening
// to prose. See ProblemInfo.
func (r Result[T]) ResultProblems() []Error { return r.Errors }
func (r Result[T]) ResultPayload() any {
	if r.Payload == nil {
		return nil
	}
	return *r.Payload
}

// ProblemInfo is the optional interface a binding checks for structured errors, alongside the
// ResultInfo it already holds. Optional, and checked with a type assertion, for the same reason
// ResultIsSuccessful is: adding a method to ResultInfo itself would break every external
// implementation of it, and a binding that only renders messages needs nothing new.
type ProblemInfo interface {
	ResultProblems() []Error
}

// ProblemDocumentInfo is the optional interface a binding checks for an application-authored problem
// document (see ProblemResult). A binding that finds one must emit it as-is; one that does not,
// or that finds nil, derives the document from the status as usual.
type ProblemDocumentInfo interface {
	ResultProblemDocument() *Problem
}

var (
	_ ResultInfo          = Result[struct{}]{}
	_ ProblemInfo         = Result[struct{}]{}
	_ ProblemDocumentInfo = Result[struct{}]{}
)

// ProblemsOf returns the structured errors of a type-erased result: the ProblemInfo view when the
// implementation offers one, and its messages wrapped as Message-only errors when it does not.
//
// Every place that rebuilds a typed Result from a ResultInfo - the in-process client, the test
// host, each outbound client - needs exactly this, and each of them writing its own type assertion
// is how one of them ends up quietly flattening field and code back to prose.
func ProblemsOf(result ResultInfo) []Error {
	if problems, ok := result.(ProblemInfo); ok {
		return problems.ResultProblems()
	}
	return errorsFromMessages(result.ResultErrors())
}

func success[T any](status Status, payload T) Result[T] {
	return Result[T]{Status: status, Payload: &payload}
}

// SetResult builds a Result whose success classification is set explicitly, decoupled from the
// status class. The intended use is the reserved health check returning StatusServiceUnavailable -
// so an HTTP probe sees 503 and a load balancer drains the instance - while still rendering its
// report body (successful=true) rather than an error payload. For ordinary results prefer Ok/Fail
// and the status-derived default; reach for this only when the transport outcome and the body's
// meaning genuinely diverge.
func SetResult[T any](status Status, payload T, successful bool) Result[T] {
	return Result[T]{Status: status, Payload: &payload, successful: &successful}
}

// Ok returns a successful Result with StatusOk.
func Ok[T any](payload T) Result[T] { return success(StatusOk, payload) }

// Created returns a successful Result with StatusCreated.
func Created[T any](payload T) Result[T] { return success(StatusCreated, payload) }

// Accepted returns a successful Result with StatusAccepted.
func Accepted[T any](payload T) Result[T] { return success(StatusAccepted, payload) }

// Updated returns a successful Result with StatusUpdated.
func Updated[T any](payload T) Result[T] { return success(StatusUpdated, payload) }

// Deleted returns a successful Result with StatusDeleted.
func Deleted[T any](payload T) Result[T] { return success(StatusDeleted, payload) }

// Ignored returns a successful Result with StatusIgnored - handled deliberately, not an error.
func Ignored[T any](payload T) Result[T] { return success(StatusIgnored, payload) }

// Fail returns a failed Result with the given status and error messages. The result is always
// unsuccessful - even for an application-defined status that IsFailure does not recognise - which
// is what makes a custom failure status nack/redeliver on a queue and render its errors rather than
// being mistaken for a success. Panics if status is in the framework success class, since that
// would produce a self-contradictory Result.
func Fail[T any](status Status, errors ...string) Result[T] {
	if status.IsSuccess() {
		panic("benzene: Fail called with a success-class status " + string(status))
	}
	return FailWith[T](status, errorsFromMessages(errors)...)
}

// FailWith is Fail with structured errors: the same status rules, but each error may carry the
// Field it came from and the Code of the rule that rejected it, and both travel all the way to the
// caller's problem document. Prefer plain Fail when there is nothing to add beyond the message.
func FailWith[T any](status Status, errors ...Error) Result[T] {
	if status.IsSuccess() {
		panic("benzene: FailWith called with a success-class status " + string(status))
	}
	unsuccessful := false
	return Result[T]{Status: status, Errors: errors, successful: &unsuccessful}
}

// ProblemResult returns a failed Result carrying an application-authored problem document, which
// the wire edge emits verbatim rather than deriving one from the status. Use it when the service
// owns its own problem vocabulary and wants its own `type` URI to reach the caller; for everything
// else Fail and FailWith derive the right document from the §3.1 registry.
//
// Panics if problem.BenzeneStatus is empty: a problem document with no status cannot be classified
// by anything downstream, so there is no sensible result to build from it.
func ProblemResult[T any](problem Problem) Result[T] {
	if problem.BenzeneStatus == "" {
		panic("benzene: ProblemResult requires problem.BenzeneStatus - a problem document with no status cannot be classified")
	}
	unsuccessful := false
	return Result[T]{
		Status:     Status(problem.BenzeneStatus),
		Errors:     problem.Errors,
		successful: &unsuccessful,
		problem:    &problem,
	}
}

// ResultProblemDocument returns the application-authored problem document, or nil when the result
// carries none and the wire edge should derive one from the status. See ProblemDocumentInfo.
func (r Result[T]) ResultProblemDocument() *Problem { return r.problem }

// ValidationErrorWith returns a failed Result with StatusValidationError and structured errors.
// Validation is where Field and Code are nearly always known - a schema validator produces exactly
// this shape - so it gets the shorthand; any other status goes through FailWith.
func ValidationErrorWith[T any](errors ...Error) Result[T] {
	return FailWith[T](StatusValidationError, errors...)
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
// safe for idempotent operations (unlike StatusServiceUnavailable, WithRetry does not
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
