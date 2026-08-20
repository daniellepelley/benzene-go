# Message results

A Benzene handler returns its outcome as a value, not by throwing. That value is a
[`benzene.Result[T]`](https://benzene.app/docs/specification/core-concepts.html) — a status, an
optional payload, and any error messages. A transport binding translates the status into that
transport's native failure signal (an HTTP code, a gRPC code, a queue ack/nack); the handler never
names one. This page is the reference for that type and the status vocabulary in Go.

The concepts here are language-neutral and defined once for every port on the website — see
[Wire contracts](https://benzene.app/docs/specification/wire-contracts.html) for the canonical
status vocabulary and the per-protocol mapping tables. This page shows the Go shape.

## The one idea

A handler is a `func(context.Context, TReq) benzene.Result[TRes]`. Whatever happens — success,
a client mistake, a missing record, a downstream outage — you *return* it:

```go
func getOrder(_ context.Context, req getOrderRequest) benzene.Result[orderResponse] {
	order, ok := store.Lookup(req.ID)
	if !ok {
		return benzene.NotFound[orderResponse]("order " + req.ID + " not found")
	}
	return benzene.Ok(orderResponse{Order: order})
}
```

`benzene.Ok(...)` carries the payload; `benzene.NotFound[...](...)` carries error messages. Both are
ordinary values of the same type, `benzene.Result[orderResponse]`. The binding decides what a
`not-found` status means on the wire — the handler stays transport-agnostic.

## `Result[T]`

Defined in `result.go`:

```go
type Result[T any] struct {
	Status  Status  // the Benzene status (see the vocabulary below)
	Payload *T      // present on success; a pointer so "absent" is representable
	Errors  []Error // structured errors, populated on failure
	// unexported: an explicit success flag (SetResult) and an authored problem document
	// (ProblemResult)
}
```

The three exported fields are the result's data. `Payload` is a pointer so that "no payload" is
distinct from `T`'s own zero value — on a failure it is `nil`.

`Error` carries a `Message` and, when the producer knows them, the `Field` the value came from and
the `Code` of the rule that rejected it:

```go
type Error struct {
	Message string
	Field   string
	Code    string
}
```

It is an **alias** of `wire.ProblemError`, so the value a handler builds is the value that reaches
the wire — there is no second shape to keep in step. The plain-string constructors (`Fail`,
`NotFound`, …) fill in `Message` only; `FailWith` and `ValidationErrorWith` take `Error` values.

### Reading a result

```go
r := getOrder(ctx, req)

r.Status                 // benzene.Status, e.g. benzene.StatusOk
r.IsSuccessful()         // bool — see the classification rule below
r.Errors                 // []benzene.Error, populated on failure
r.ResultErrors()         // []string — just the messages, for code that only wants prose
if r.Payload != nil {    // nil on failure; check before dereferencing
	use(*r.Payload)
}
```

`IsSuccessful()` derives from the status class unless an explicit flag was set (via
[`SetResult`](#setresult--decouple-success-from-status)): it returns `!r.Status.IsFailure()`. So a framework success status and an
*application-defined* status both count as successful and carry their payload, while only a
framework **failure** status does not — that is the extensibility promise that custom statuses flow
through untouched.

### The type-erased view: `ResultInfo`

The registry stores handlers behind a non-generic dispatch signature, so the pipeline and transport
bindings recover a result's parts through an interface rather than the concrete `Result[T]` (whose
`T` they cannot name). Every `Result[T]` implements it:

```go
type ResultInfo interface {
	ResultStatus() Status
	ResultErrors() []string
	ResultPayload() any // the payload as any, or nil if absent
}
```

`ResultErrors()` deliberately still returns `[]string`: every binding that only ever wanted messages
is unaffected by structured errors existing.

Three optional interfaces sit alongside it, each checked with a type assertion so that adding one
never breaks an external implementation of `ResultInfo`:

| Interface | Method | What it recovers |
|---|---|---|
| — | `ResultIsSuccessful() bool` | an explicit success flag (`SetResult`) surviving type erasure |
| `ProblemInfo` | `ResultProblems() []Error` | the structured errors, so a `field` and a `code` are not flattened to prose |
| `ProblemDocumentInfo` | `ResultProblemDocument() *Problem` | an application-authored problem document (`ProblemResult`), emitted verbatim |

`ProblemsOf(result)` is the helper every rebuild-a-typed-result site should use: it takes the
`ProblemInfo` view when there is one and wraps the messages when there is not, so no caller writes
its own assertion and quietly drops the structure.

## Constructors

Every constructor lives in `result.go`. The success constructors take a payload; the failure
constructors take variadic error strings (`errors ...string`).

### Success — carry a payload

| Constructor | Signature | Status |
|---|---|---|
| `Ok` | `Ok[T any](payload T) Result[T]` | `ok` |
| `CreatedResult` | `CreatedResult[T any](payload T) Result[T]` | `created` |
| `Accepted` | `Accepted[T any](payload T) Result[T]` | `accepted` |
| `Updated` | `Updated[T any](payload T) Result[T]` | `updated` |
| `Deleted` | `Deleted[T any](payload T) Result[T]` | `deleted` |
| `Ignored` | `Ignored[T any](payload T) Result[T]` | `ignored` — handled deliberately, not an error |

`Ok` infers `T` from its argument (`benzene.Ok(orderResponse{...})`); the rest do too. Note the name
is `CreatedResult`, not `Created`.

### Failure — carry error messages

| Constructor | Signature | Status |
|---|---|---|
| `BadRequest` | `BadRequest[T any](errors ...string) Result[T]` | `bad-request` |
| `ValidationError` | `ValidationError[T any](errors ...string) Result[T]` | `validation-error` |
| `Unauthorized` | `Unauthorized[T any](errors ...string) Result[T]` | `unauthorized` |
| `Forbidden` | `Forbidden[T any](errors ...string) Result[T]` | `forbidden` |
| `NotFound` | `NotFound[T any](errors ...string) Result[T]` | `not-found` |
| `Conflict` | `Conflict[T any](errors ...string) Result[T]` | `conflict` |
| `TooManyRequests` | `TooManyRequests[T any](errors ...string) Result[T]` | `too-many-requests` — throttled; transient, retry after backoff |
| `Timeout` | `Timeout[T any](errors ...string) Result[T]` | `timeout` — downstream deadline elapsed; outcome unknown |
| `NotImplemented` | `NotImplemented[T any](errors ...string) Result[T]` | `not-implemented` |
| `ServiceUnavailable` | `ServiceUnavailable[T any](errors ...string) Result[T]` | `service-unavailable` |
| `UnexpectedError` | `UnexpectedError[T any](errors ...string) Result[T]` | `unexpected-error` |

The failure constructors need an explicit type argument, because `T` can't be inferred from the
error strings: `benzene.NotFound[orderResponse]("...")`. A failure result has a `nil` payload and
`IsSuccessful() == false`.

### `Fail` — the errors-based failure constructor

Every failure constructor above delegates to `Fail`, which is public for raising a failure with any
status — including an application-defined one:

```go
func Fail[T any](status Status, errors ...string) Result[T]
```

Because it takes error strings, a `Fail` result is **always** unsuccessful, even for a custom status
that `IsFailure` does not recognise — which is what makes a custom failure status nack/redeliver on
a queue and render its errors rather than being mistaken for a success. `Fail` **panics** if given a
framework success-class status, since that would produce a self-contradictory result.

### `SetResult` — decouple success from status

```go
func SetResult[T any](status Status, payload T, successful bool) Result[T]
```

`SetResult` sets the success classification explicitly, independent of the status class. The intended
use is the reserved health check returning `StatusServiceUnavailable` — so an HTTP probe sees `503`
and a load balancer drains the instance — while still rendering its report body (`successful=true`)
rather than an error payload. For ordinary results prefer the constructors above and the
status-derived default; reach for this only when the transport outcome and the body's meaning
genuinely diverge.

## The status vocabulary

`Status` (in `status.go`) is a wire-level string, not a closed enum, so applications can extend it.
The framework-defined values are held verbatim to the spec's case-sensitive
lowercase-kebab-case wire contract:

```go
type Status string

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
```

Three classifiers report which class a status belongs to:

- `Status.IsSuccess()` — one of the six framework success statuses (`ok`, `created`, `accepted`,
  `updated`, `deleted`, `ignored`).
- `Status.IsFailure()` — one of the eleven framework failure statuses. An application-defined or
  empty status is **not** assumed to be a failure — this is what keeps custom statuses flowing
  through the pipeline and envelope untouched.
- `Status.IsKnown()` — part of the framework vocabulary (success or failure).

## Transport mapping

A status is protocol-neutral; each binding maps it to a native code. These mapping packages are
zero-dependency and implement the spec's tables directly.

### HTTP — `httpstatus`

`httpstatus.ToHTTP(status benzene.Status) int` implements
[wire-contracts §4.1](https://benzene.app/docs/specification/wire-contracts.html). An unrecognized,
application-defined, or empty status (and `unexpected-error`) maps to `500`:

| Benzene status | HTTP code |
|---|---|
| `ok`, `ignored` | 200 |
| `created` | 201 |
| `accepted` | 202 |
| `updated`, `deleted` | 204 |
| `bad-request` | 400 |
| `unauthorized` | 401 |
| `forbidden` | 403 |
| `not-found` | 404 |
| `conflict` | 409 |
| `validation-error` | 422 |
| `too-many-requests` | 429 |
| `not-implemented` | 501 |
| `service-unavailable` | 503 |
| `timeout` | 504 |
| `unexpected-error`, unknown, empty | 500 |

The package also provides `httpstatus.FromHTTP(code int) benzene.Status` for the reverse direction —
used by an HTTP outbound client reading a response.

### gRPC — `grpcstatus`

`grpcstatus.ToGRPC(status benzene.Status) int` implements
[wire-contracts §4.2](https://benzene.app/docs/specification/wire-contracts.html). Codes are the raw
numeric gRPC status codes (a `gRPC` binding wraps the result as `codes.Code(grpcstatus.ToGRPC(...))`).
All success-class statuses collapse to `OK (0)`; an unrecognized or empty status (and
`unexpected-error`) maps to `Internal (13)`:

| Benzene status | gRPC code |
|---|---|
| `ok`, `ignored`, `created`, `accepted`, `updated`, `deleted` | `OK` (0) |
| `bad-request`, `validation-error` | `InvalidArgument` (3) |
| `unauthorized` | `Unauthenticated` (16) |
| `forbidden` | `PermissionDenied` (7) |
| `not-found` | `NotFound` (5) |
| `conflict` | `AlreadyExists` (6) |
| `too-many-requests` | `ResourceExhausted` (8) |
| `timeout` | `DeadlineExceeded` (4) |
| `not-implemented` | `Unimplemented` (12) |
| `service-unavailable` | `Unavailable` (14) |
| `unexpected-error`, unknown, empty | `Internal` (13) |

`grpcstatus.FromGRPC(code int) benzene.Status` maps back — used by a gRPC outbound client when no
`benzene-status` trailer is present (a trailer, when present, wins verbatim).

## See also

- [Getting started](getting-started.md) — where handlers return these results end to end over HTTP.
- [Wire contracts](https://benzene.app/docs/specification/wire-contracts.html) — the canonical
  status vocabulary and the full per-protocol mapping tables every port implements.
- [Core concepts](https://benzene.app/docs/specification/core-concepts.html) — the result concept in
  the language-neutral model.
