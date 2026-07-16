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
	"github.com/daniellepelley/benzene-go/wire"
)

// Client sends outbound Benzene messages over HTTP as wire-contracts.md envelopes. Client
// satisfies client.Sender, so it can be wrapped in client.CorrelationDecorator/RetryDecorator
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
// A transport-level failure (network error, non-2xx HTTP from the transport itself rather than
// the envelope's own statusCode, a malformed response) maps to ServiceUnavailable, per
// transport-bindings §1.7's failure rule: the caller always gets a Result back, never a Go
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

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return benzene.ServiceUnavailable[json.RawMessage](fmt.Sprintf("outbound request returned HTTP %d", httpResp.StatusCode))
	}

	resp, err := wire.UnmarshalResponse(respBody)
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("failed to parse outbound envelope response: " + err.Error())
	}

	return toResult(resp)
}

func toResult(resp wire.Response) benzene.Result[json.RawMessage] {
	status := benzene.Status(resp.StatusCode)
	if !status.IsSuccess() {
		errPayload, parseErr := wire.UnmarshalErrorPayload([]byte(resp.Body))
		if parseErr != nil || errPayload.Detail == "" {
			return benzene.Fail[json.RawMessage](status)
		}
		return benzene.Fail[json.RawMessage](status, errPayload.Detail)
	}

	if resp.Body == "" {
		return benzene.Result[json.RawMessage]{Status: status}
	}
	payload := json.RawMessage(resp.Body)
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
