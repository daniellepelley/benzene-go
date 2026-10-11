// Package httpclient is the HTTP outbound client of
// daniellepelley/Benzene's docs/specification/transport-bindings.md §2 ("Outbound clients"):
// one interface - sendMessage(topic, headers, message) -> result - over HTTP, talking the
// wire-contracts.md envelope to a target service's envelope endpoint (e.g. one built with
// httpbinding.EnvelopeHandler).
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpstatus"
	"github.com/daniellepelley/benzene-go/wire"
)

// Client sends outbound Benzene messages over HTTP as wire-contracts.md envelopes. Client
// satisfies client.Sender, so it can be wrapped in client.WithCorrelationID/WithRetry
// (or any other Sender decorator) without modification.
type Client struct {
	// Endpoint is the full URL of the target service's envelope endpoint (e.g.
	// "http://orders:8080/invoke").
	Endpoint string
	// HTTPClient is the underlying HTTP client used to send requests. Defaults to
	// http.DefaultClient when nil.
	HTTPClient *http.Client
}

// NewClient returns a Client that POSTs envelopes to endpoint using http.DefaultClient.
func NewClient(endpoint string) *Client {
	return &Client{Endpoint: endpoint}
}

var _ client.Sender = (*Client)(nil)

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// Send implements the outbound-client contract: forward topic/headers/message to Endpoint and
// map the target's outcome back to a Result[json.RawMessage]. Headers are forwarded onto the
// wire envelope's own headers field verbatim - this transport-bindings §2's correlation/trace
// propagation requirement, and this transport has no separate native metadata channel to also
// populate, since it already speaks the envelope.
//
// The response is read the way transport-bindings.md §1 asks of an envelope-over-HTTP client:
// the envelope in the body is the outcome, whatever the HTTP status line says (a conformant
// envelope endpoint answers 200 for every envelope it processed, failures included), and it is
// read by envelope.ToResult - isSuccessful decides, never the status text. Only when the body is
// not an envelope (a gateway's error page, a proxy's 502, the wrong URL) does the HTTP status
// decide, through the reverse table of wire-contracts.md §4.1 (httpstatus.FromHTTP): a failure
// code becomes that status, with the errors of the body's problem document when it carries one and
// a message naming the code otherwise, and a success code that status with the body as the payload
// when it is JSON.
//
// A transport-level failure (a network error, an unreadable response) maps to ServiceUnavailable,
// per transport-bindings §1.7's failure rule: the caller always gets a Result back, never a Go
// error. Use Unmarshal to convert the raw payload into an application type.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	reqBody, err := wire.MarshalRequest(wire.Request{Topic: topic.String(), Headers: headers, Body: string(message)})
	if err != nil {
		// wire.Request is a plain struct of strings and a string map - Marshal cannot fail on
		// it in practice, but degrade gracefully rather than panic if it somehow ever does.
		return benzene.ServiceUnavailable[json.RawMessage]("failed to serialize outbound request: " + err.Error())
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("failed to build outbound request: " + err.Error())
	}
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("outbound request failed: " + err.Error())
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("failed to read outbound response: " + err.Error())
	}

	if resp, err := wire.UnmarshalResponse(respBody); err == nil && resp.StatusCode != "" {
		return envelope.ToResult(resp)
	}
	return fromHTTPStatus(httpResp.StatusCode, respBody)
}

// fromHTTPStatus is the fallback for a response whose body is not an envelope: the HTTP status
// read through the reverse table of wire-contracts.md §4.1.
//
// A failure whose body is a problem document - an application/problem+json response from a
// route-mapped HTTP binding, or a JSON problem from anything in front of one (§4.1: clients accept
// both content types) - is read like the problem inside a failure envelope: its errors are rebuilt
// with field and code intact (core-concepts.md §12, error fidelity), errors when present, else
// detail as one opaque message. Only a body that yields no error at all (a gateway's HTML page, an
// empty body, a JSON object with neither errors nor detail) falls back to a message naming the code.
func fromHTTPStatus(code int, body []byte) benzene.Result[json.RawMessage] {
	status := httpstatus.FromHTTP(code)
	if status.IsFailure() {
		if problem, err := wire.UnmarshalErrorPayload(body); err == nil {
			if problems := problem.Problems(); len(problems) > 0 {
				return benzene.FailWith[json.RawMessage](status, problems...)
			}
		}
		return benzene.Fail[json.RawMessage](status, fmt.Sprintf("outbound request returned HTTP %d with no response envelope", code))
	}
	if !json.Valid(body) {
		return benzene.Result[json.RawMessage]{Status: status}
	}
	payload := json.RawMessage(body)
	return benzene.Result[json.RawMessage]{Status: status, Payload: &payload}
}

// Unmarshal converts a Result[json.RawMessage] returned by Send into a Result[T] by JSON-
// unmarshaling its payload (if present) into T. This lets a caller work with Send's fixed
// signature (transport-bindings §2 requires exactly one send interface) while still getting a
// strongly-typed result for its own response type.
func Unmarshal[T any](result benzene.Result[json.RawMessage]) (benzene.Result[T], error) {
	if result.Payload == nil {
		return benzene.Result[T]{Status: result.Status, Errors: result.Errors}, nil
	}
	var typed T
	if err := json.Unmarshal(*result.Payload, &typed); err != nil {
		return benzene.Result[T]{}, err
	}
	return benzene.Result[T]{Status: result.Status, Payload: &typed, Errors: result.Errors}, nil
}
