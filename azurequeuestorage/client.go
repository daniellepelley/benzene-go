// Package azurequeuestorage is the outbound Azure Queue Storage client, the Go port of
// Benzene.Clients.Azure.QueueStorage's QueueStorageBenzeneMessageClient (its
// QueueStorageContextConverter). Client satisfies client.Sender, so it composes with
// client.WithCorrelationID/WithRetry like any other outbound client. Outbound-only:
// Queue Storage has no request/response beyond a send acknowledgement, and inbound Queue
// Storage delivery is the azurefunctions QueueHandler.
package azurequeuestorage

import (
	"context"
	"encoding/json"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// EnqueueMessageAPI is the single Queue Storage SDK method Client depends on. Depending on this
// narrow interface, rather than the concrete *azqueue.QueueClient, makes Client testable with a
// fake - no real Azure calls (and no credential wiring) needed in tests. *azqueue.QueueClient
// (constructed with azqueue.NewQueueClient/NewQueueClientFromConnectionString) satisfies it
// as-is.
type EnqueueMessageAPI interface {
	EnqueueMessage(ctx context.Context, content string, o *azqueue.EnqueueMessageOptions) (azqueue.EnqueueMessagesResponse, error)
}

// Client publishes outbound Benzene messages to an Azure Storage queue. It satisfies
// client.Sender, so it can be wrapped in client.WithCorrelationID/WithRetry like any
// other Sender.
type Client struct {
	API EnqueueMessageAPI
}

// NewClient returns a Client publishing via api (typically an *azqueue.QueueClient bound to a
// single queue and constructed from your own credentials).
func NewClient(api EnqueueMessageAPI) *Client {
	return &Client{API: api}
}

// Send enqueues message as a wire.Request envelope, matching QueueStorageContextConverter
// exactly: a Storage Queue message is a single string with no properties/attributes bag, so the
// topic and headers cannot travel natively (as they do on SQS/SNS) - the whole {topic, headers,
// body} envelope IS the message text (the "or envelope" transport-bindings convention), and the
// inbound side (azurefunctions.QueueHandler over an EnvelopeConverter) parses it back. Body is
// message verbatim - it is already the pre-serialized JSON payload (wire-contracts.md §1.1).
//
// The azqueue SDK sends the content string verbatim (it does NOT base64-encode, unlike the
// legacy .NET QueueClient's historical default), so the envelope JSON is stored as-is; a
// consumer must therefore not base64-decode. A successful enqueue maps to StatusAccepted -
// wire-contracts.md §3's "Accepted for asynchronous processing" fits exactly, as Queue Storage
// has no response beyond a send acknowledgement. A transport-level failure maps to
// ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	envelope, err := wire.MarshalRequest(wire.Request{
		Topic:   topic.String(),
		Headers: headers,
		Body:    string(message),
	})
	if err != nil {
		// wire.Request is a fixed struct of strings and a string->string map, so encoding/json
		// cannot fail marshaling it in practice; degrade to service-unavailable rather than
		// panic if it somehow ever does.
		return benzene.ServiceUnavailable[json.RawMessage]("azurequeuestorage: marshal envelope failed: " + err.Error())
	}

	if _, err := c.API.EnqueueMessage(ctx, string(envelope), nil); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("azurequeuestorage: enqueue failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
