// Package httpstatus implements the Benzene<->HTTP status mapping tables of
// daniellepelley/Benzene's docs/specification/wire-contracts.md §4.1.
package httpstatus

import (
	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// ToHTTP maps a Benzene status to its HTTP status code (wire-contracts.md §4.1, forward).
//
// A status in the §3 vocabulary maps by its own row regardless of anything else. The optional
// isSuccessful decides only for an APPLICATION-DEFINED status, which has no row: a failed one
// maps to 500, and one carried on a result explicitly marked successful (the
// Set(status, payload, isSuccessful) escape hatch) maps to 200 rather than being reported as a
// server error. Omitted, an unknown status is treated as a failure - the safe default for a
// caller that cannot tell. Variadic so every existing single-argument call still compiles.
func ToHTTP(status benzene.Status, isSuccessful ...bool) int {
	switch status {
	case benzene.StatusOk, benzene.StatusIgnored:
		return 200
	case benzene.StatusCreated:
		return 201
	case benzene.StatusAccepted:
		return 202
	case benzene.StatusUpdated, benzene.StatusDeleted:
		return 204
	case benzene.StatusBadRequest:
		return 400
	case benzene.StatusUnauthorized:
		return 401
	case benzene.StatusForbidden:
		return 403
	case benzene.StatusNotFound:
		return 404
	case benzene.StatusConflict:
		return 409
	case benzene.StatusValidationError:
		return 422
	case benzene.StatusTooManyRequests:
		return 429
	case benzene.StatusNotImplemented:
		return 501
	case benzene.StatusServiceUnavailable:
		return 503
	case benzene.StatusTimeout:
		return 504
	case benzene.StatusUnexpectedError:
		return 500
	default: // an application-defined status, or empty
		if len(isSuccessful) > 0 && isSuccessful[0] {
			return 200
		}
		return 500
	}
}

// FromHTTP maps an HTTP status code to a Benzene status (wire-contracts.md §4.1, reverse) -
// used by an HTTP outbound client reading a response.
func FromHTTP(code int) benzene.Status {
	switch code {
	case 200:
		return benzene.StatusOk
	case 201:
		return benzene.StatusCreated
	case 202:
		return benzene.StatusAccepted
	case 204:
		return benzene.StatusDeleted
	case 400:
		return benzene.StatusBadRequest
	case 401:
		return benzene.StatusUnauthorized
	case 403:
		return benzene.StatusForbidden
	case 404:
		return benzene.StatusNotFound
	case 408:
		return benzene.StatusTimeout
	case 409:
		return benzene.StatusConflict
	case 422:
		return benzene.StatusValidationError
	case 429:
		return benzene.StatusTooManyRequests
	case 501:
		return benzene.StatusNotImplemented
	case 502, 503:
		return benzene.StatusServiceUnavailable
	case 504:
		return benzene.StatusTimeout
	default:
		return benzene.StatusUnexpectedError
	}
}

// Response renders a wire.Response as the three things an HTTP binding has to send: the response
// code, the body, and the headers. Every HTTP binding in this port goes through it, so none of
// them can quietly disagree about wire-contracts.md §4.1.
//
// The code is ToHTTP's, honouring the response's own IsSuccessful (§1.2) rather than re-deriving
// success from the status. On a failure whose body is a problem document, the document's `status`
// member is filled in with that same code - §4.1's "MUST equal the code actually being sent" -
// and the content type states the body for what it is. The transport-neutral document leaves the
// member out (§1.3), because most transports have no HTTP response for it to equal, so an HTTP
// binding is the only place it can be filled in.
//
// The returned header map is a copy: the caller may write to it, and resp is left untouched.
func Response(resp wire.Response) (code int, body string, headers map[string]string) {
	successful := !benzene.Status(resp.StatusCode).IsFailure()
	if resp.IsSuccessful != nil {
		successful = *resp.IsSuccessful
	}
	code = ToHTTP(benzene.Status(resp.StatusCode), successful)

	headers = make(map[string]string, len(resp.Headers)+1)
	for name, value := range resp.Headers {
		headers[name] = value
	}

	body = resp.Body
	if !successful && body != "" {
		if withStatus, ok := wire.WithHTTPStatus(body, code); ok {
			body = withStatus
			headers["content-type"] = "application/problem+json"
		}
	}
	return code, body, headers
}
