// Package azureeventgrid is the outbound Azure Event Grid client, the Go port of
// Benzene.Clients.Azure.EventGrid's EventGridBenzeneMessageClient (its CloudEvents 1.0
// EventGridContextConverter, the schema Benzene's Event Grid ingress prefers). Client
// satisfies client.Sender, so it composes with client.WithCorrelationID/WithRetry
// like any other outbound client. Outbound-only: Event Grid has no request/response beyond a
// send acknowledgement, and inbound Event Grid delivery is the azurefunctions EventGridHandler.
package azureeventgrid

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/messaging"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// PublishCloudEventsAPI is the single Event Grid SDK method Client depends on. Depending on
// this narrow interface, rather than the concrete *azeventgrid.Client, makes Client testable
// with a fake - no real Azure calls (and no credential wiring) needed in tests.
// *azeventgrid.Client (the Basic-tier publisher from
// github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid, constructed with
// azeventgrid.NewClientWithSharedKeyCredential/NewClient) satisfies it as-is.
type PublishCloudEventsAPI interface {
	PublishCloudEvents(ctx context.Context, events []messaging.CloudEvent, options *azeventgrid.PublishCloudEventsOptions) (azeventgrid.PublishCloudEventsResponse, error)
}

// Client publishes outbound Benzene messages to an Azure Event Grid topic as CloudEvents 1.0.
// It satisfies client.Sender, so it can be wrapped in client.WithCorrelationID/
// WithRetry like any other Sender.
type Client struct {
	API PublishCloudEventsAPI
	// Source is the CloudEvent "source" attribute - the publishing service's identity (e.g.
	// "com.example.orders"), identifying the context the event happened in. The caller owns it,
	// exactly like awseventbridge.Client's source and cloudevents.FromRequest's source argument.
	// Event Grid requires a non-empty source; a Send with an empty Source fails to
	// service-unavailable rather than publishing an invalid event.
	Source string
}

// NewClient returns a Client publishing via api (typically an *azeventgrid.Client constructed
// from your own credentials) with the given source identity.
func NewClient(api PublishCloudEventsAPI, source string) *Client {
	return &Client{API: api, Source: source}
}

// Send publishes message as one CloudEvent, matching the main repo's Event Grid convention and
// EventGridContextConverter exactly: Type is the topic (the attribute Benzene's Event Grid
// ingress routes on), Data is message as raw JSON (content type application/json), and each
// header travels as a CloudEvent extension attribute under its lower-cased name - Event Grid's
// CloudEvents schema exposes extension attributes as the native per-event metadata channel, so
// (unlike awseventbridge, whose EventBridge detail has no such channel) headers are NOT embedded
// in the payload. A successful publish maps to StatusAccepted - wire-contracts.md §3's
// "Accepted for asynchronous processing" fits exactly, as Event Grid has no response beyond a
// send acknowledgement. A build failure (empty Source) or a transport-level failure maps to
// ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	var extensions map[string]any
	if len(headers) > 0 {
		extensions = make(map[string]any, len(headers))
		for k, v := range headers {
			// Lower-case the extension name, matching EventGridContextConverter's
			// ExtensionAttributes[header.Key.ToLowerInvariant()] - CloudEvents extension names
			// are lower-case. No further sanitizing, to stay byte-for-byte with the .NET client.
			extensions[strings.ToLower(k)] = v
		}
	}

	contentType := "application/json"
	// json.RawMessage keeps message as the CloudEvent's JSON "data" attribute verbatim; a plain
	// []byte would be base64-encoded into "data_base64" by the SDK (see messaging.CloudEvent's
	// MarshalJSON) - message is already the pre-serialized JSON body (wire-contracts.md §1.1).
	// Normalize an empty body to JSON null: a json.RawMessage over an empty (but non-nil) slice
	// marshals to nothing, which makes the whole CloudEvent fail to serialize (invalid JSON) at
	// publish time - guard it to a valid CloudEvents "data": null instead.
	data := json.RawMessage(message)
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	event, err := messaging.NewCloudEvent(c.Source, topic.String(), data, &messaging.CloudEventOptions{
		DataContentType: &contentType,
		Extensions:      extensions,
	})
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("azureeventgrid: build event failed: " + err.Error())
	}

	if _, err := c.API.PublishCloudEvents(ctx, []messaging.CloudEvent{event}, nil); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("azureeventgrid: publish failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
