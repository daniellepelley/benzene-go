// Package wire implements the transport-neutral message envelope and status vocabulary
// defined in daniellepelley/Benzene's docs/specification/wire-contracts.md. Everything in
// this package crosses a process boundary and is what makes two Benzene implementations -
// in any two languages, on any two vendors - interoperable, so it deliberately has no
// dependency on the rest of this module: it's pure wire-format data types plus (de)
// serialization, reusable by every transport binding and outbound client.
package wire

import (
	"encoding/json"
	"strings"
)

// Request is the inbound message envelope (wire-contracts.md §1.1), used whenever a
// Benzene client sends to a Benzene service over a transport with no richer native
// contract.
type Request struct {
	// Topic is the topic id (docs/specification/core-concepts.md §2). Version, when used,
	// travels as a header, not as part of this field.
	Topic string `json:"topic"`
	// Headers is a flat string->string map - no nested values.
	Headers map[string]string `json:"headers"`
	// Body is the message payload, pre-serialized as a string (JSON by default), not an
	// inline object - this keeps the envelope schema fixed regardless of payload schema.
	Body string `json:"body"`
}

// Response is the outbound message envelope (wire-contracts.md §1.2).
type Response struct {
	// StatusCode is a status vocabulary value (see Status in status.go) - the Benzene
	// status, not an HTTP code.
	StatusCode string `json:"statusCode"`
	// Headers are response headers, including "content-type" when set.
	Headers map[string]string `json:"headers"`
	// Body is the pre-serialized response payload: on success, the handler's response
	// payload; on failure, the serialized ErrorPayload (§1.3).
	Body string `json:"body"`
	// IsSuccessful, when set, states outright whether the result succeeded (§1.2), rather than
	// leaving a reader to classify StatusCode against a vocabulary it may not fully share. It is
	// what lets an application-defined status be carried on a SUCCESSFUL result - the
	// Set(status, payload, isSuccessful) escape hatch - without a peer mistaking it for a failure.
	// A pointer so it is omitted rather than emitted as false when a writer does not set it, and
	// so a reader can tell "absent" (fall back to classifying the status) from "explicitly false".
	IsSuccessful *bool `json:"isSuccessful,omitempty"`
}

// ErrorPayload is the RFC 9457 problem document written as a Response's Body when the result is
// unsuccessful (wire-contracts.md §1.3). A genuine problem document, not a problem-shaped struct.
//
// The member that used to be named Status carried the Benzene status STRING, colliding with RFC
// 9457's own status, which is the integer HTTP response code. The 2026-08 revision resolved that
// by rename rather than by dropping the RFC alignment: the Benzene status now travels as
// BenzeneStatus, and Status is the integer HTTP code - present only on an HTTP binding, omitted
// entirely (not zero, not null) wherever no HTTP response exists.
type ErrorPayload struct {
	// Type is the §3.1 registry URI for the status, or an application's own URI. An opaque
	// identifier: readers compare by string equality and never dereference it.
	Type string `json:"type,omitempty"`
	// Title is a short human summary of the type, fixed per type. Never asserted by conformance.
	Title string `json:"title,omitempty"`
	// Status is the integer HTTP response code, on HTTP bindings only (§4.1), where it MUST equal
	// the code actually sent. Omitted everywhere else - a pointer so that zero is never mistaken
	// for "HTTP 0". Benzene clients MUST NOT classify a result from this member: classification is
	// envelope-first (§1.2).
	Status *int `json:"status,omitempty"`
	// Detail is the result's error messages, joined with ", " - the compatibility member every
	// existing reader can keep using on its own. A missing/empty Detail with no Errors yields an
	// error-free failed result on the reading side.
	Detail string `json:"detail,omitempty"`
	// Instance is optional and application-owned. The framework never fabricates it.
	Instance string `json:"instance,omitempty"`
	// BenzeneStatus is the §3 status string, mirroring the envelope's StatusCode. Required: it is
	// the transport-neutral discriminator, present regardless of whether Status is.
	BenzeneStatus string `json:"benzeneStatus,omitempty"`
	// Errors, when present, is authoritative and ordered - it supersedes the withdrawn "recover
	// errors by splitting detail on ', '" rule, which was never safe because messages contain
	// commas. A reader with no Errors treats Detail as a single opaque message.
	Errors []ProblemError `json:"errors,omitempty"`
}

// ProblemError is one entry of ErrorPayload.Errors (wire-contracts.md §1.3).
type ProblemError struct {
	// Message is the human-readable error message. Required.
	Message string `json:"message"`
	// Field is the producer's property path, when it has one (JSON Pointer for schema-based
	// validators, the host language's property path otherwise). Optional.
	Field string `json:"field,omitempty"`
	// Code is a machine-readable, producer-owned rule identifier, emitted verbatim - never
	// normalized or reworded by the framework. Optional.
	Code string `json:"code,omitempty"`
}

// NewErrorPayload builds the transport-neutral problem document for a status and its error
// messages: type and title from the §3.1 registry (both omitted for an application-defined
// status), detail joined with ", ", benzeneStatus always, and errors listed individually.
//
// Status is deliberately left nil. An HTTP binding sets it to the code it is actually sending
// (§4.1); every other transport omits it, because there is no HTTP response for it to equal.
func NewErrorPayload(status string, errors []string) ErrorPayload {
	problems := make([]ProblemError, 0, len(errors))
	for _, message := range errors {
		problems = append(problems, ProblemError{Message: message})
	}
	return NewProblem(status, problems)
}

// NewProblem is NewErrorPayload for errors that already carry a field and a code: same document,
// same registry lookup, same joined detail, but each error reaches the wire whole rather than
// flattened to its message. This is the constructor the dispatch path uses; NewErrorPayload is the
// message-only convenience on top of it.
func NewProblem(status string, errors []ProblemError) ErrorPayload {
	messages := make([]string, 0, len(errors))
	for _, item := range errors {
		messages = append(messages, item.Message)
	}
	payload := ErrorPayload{
		Type:          ProblemType(status),
		Title:         ProblemTitle(status),
		Detail:        strings.Join(messages, ", "),
		BenzeneStatus: status,
	}
	payload.Errors = append(payload.Errors, errors...)
	return payload
}

// Problems returns the problem's structured errors: Errors when present (authoritative and ordered),
// otherwise Detail as a single message-only error, otherwise none.
//
// This is what a client decoding a peer's problem document should use. Messages() flattens the same
// document to prose, which throws away a field and a code the peer went to the trouble of sending -
// fine for logging, wrong for rebuilding a Result.
func (e ErrorPayload) Problems() []ProblemError {
	if len(e.Errors) > 0 {
		problems := make([]ProblemError, 0, len(e.Errors))
		for _, item := range e.Errors {
			if item.Message != "" {
				problems = append(problems, item)
			}
		}
		return problems
	}
	if e.Detail != "" {
		return []ProblemError{{Message: e.Detail}}
	}
	return nil
}

// Messages returns the problem's error messages: Errors when present (authoritative and ordered),
// otherwise Detail as a single opaque message, otherwise none. See Problems for the structured
// view, which is what a client rebuilding a Result wants.
func (e ErrorPayload) Messages() []string {
	if len(e.Errors) > 0 {
		messages := make([]string, 0, len(e.Errors))
		for _, item := range e.Errors {
			if item.Message != "" {
				messages = append(messages, item.Message)
			}
		}
		return messages
	}
	if e.Detail != "" {
		return []string{e.Detail}
	}
	return nil
}

// MarshalRequest serializes r to JSON.
func MarshalRequest(r Request) ([]byte, error) {
	return json.Marshal(r)
}

// UnmarshalRequest parses JSON into a Request. Property-name matching is case-insensitive
// on read (wire-contracts.md §6), which encoding/json already does by default.
func UnmarshalRequest(data []byte) (Request, error) {
	var r Request
	err := json.Unmarshal(data, &r)
	return r, err
}

// MarshalResponse serializes r to JSON.
func MarshalResponse(r Response) ([]byte, error) {
	return json.Marshal(r)
}

// UnmarshalResponse parses JSON into a Response.
func UnmarshalResponse(data []byte) (Response, error) {
	var r Response
	err := json.Unmarshal(data, &r)
	return r, err
}

// MarshalErrorPayload serializes e to JSON.
func MarshalErrorPayload(e ErrorPayload) ([]byte, error) {
	return json.Marshal(e)
}

// UnmarshalErrorPayload parses JSON into an ErrorPayload.
//
// Tolerant of a `status` member that is not a number. RFC 9457 types it as the integer HTTP code,
// and so does this struct, but Benzene's own pre-9457 body used that name for the status STRING -
// so a peer that has not upgraded yet sends {"status":"not-found","detail":"..."}. Parsing that
// strictly would fail the whole document and throw away a perfectly readable `detail`, turning a
// version skew into lost error text. Instead the mistyped member is dropped and the rest is kept:
// the reader gets the detail, and `benzeneStatus` is simply absent, which is exactly what an old
// peer means. Readers must ignore members they do not recognize (§1.3); this extends the same
// courtesy to one they recognize but cannot use.
func UnmarshalErrorPayload(data []byte) (ErrorPayload, error) {
	var e ErrorPayload
	if err := json.Unmarshal(data, &e); err == nil {
		return e, nil
	}

	var loose map[string]json.RawMessage
	if err := json.Unmarshal(data, &loose); err != nil {
		return ErrorPayload{}, err
	}
	delete(loose, "status")

	// Unreachable in practice, and deliberately left uncovered: `loose` is a map of RawMessages that
	// json.Unmarshal just produced from `data`, so re-marshalling them cannot fail. Kept rather than
	// ignored because swallowing a Marshal error would be worse than the branch being untested.
	rest, err := json.Marshal(loose)
	if err != nil {
		return ErrorPayload{}, err
	}

	var relaxed ErrorPayload
	if err := json.Unmarshal(rest, &relaxed); err != nil {
		return ErrorPayload{}, err
	}
	return relaxed, nil
}
