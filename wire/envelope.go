// Package wire implements the transport-neutral message envelope and status vocabulary
// defined in daniellepelley/Benzene's docs/specification/wire-contracts.md. Everything in
// this package crosses a process boundary and is what makes two Benzene implementations -
// in any two languages, on any two vendors - interoperable, so it deliberately has no
// dependency on the rest of this module: it's pure wire-format data types plus (de)
// serialization, reusable by every transport binding and outbound client.
package wire

import "encoding/json"

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
}

// ErrorPayload is the problem-details-shaped error payload written as a Response's Body
// when the result is unsuccessful (wire-contracts.md §1.3).
type ErrorPayload struct {
	// Status is the Benzene status, repeated from the envelope.
	Status string `json:"status"`
	// Detail is the result's error messages, joined with ", ". A missing/empty Detail
	// yields an error-free failed result on the reading side.
	Detail string `json:"detail"`
	// Type, Title, and Instance are reserved for RFC 7807 alignment. Writers MAY emit them
	// as null or omit them; this package omits them (omitempty).
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Instance string `json:"instance,omitempty"`
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
func UnmarshalErrorPayload(data []byte) (ErrorPayload, error) {
	var e ErrorPayload
	err := json.Unmarshal(data, &e)
	return e, err
}
