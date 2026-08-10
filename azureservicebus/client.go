// Package azureservicebus is the Azure Service Bus binding for Benzene: an outbound Client that
// publishes Benzene messages to a queue or topic, and a self-hosted Worker that consumes a queue or
// subscription and dispatches each message through the pipeline. It is the Go port of
// Benzene.Clients.Azure.ServiceBus (outbound) and Benzene.Azure.ServiceBus (the self-hosted worker,
// Benzene.Azure.Function.ServiceBus being the Functions-triggered flavor covered by the
// azurefunctions package's QueueHandler).
//
// Like awssqs, it lives in its own Go module because it depends on a third-party SDK
// (github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus). Both halves depend on a narrow
// interface (SenderAPI / ReceiverAPI) rather than the concrete SDK types, so tests run against fakes
// with no live Service Bus.
//
// Routing matches the .NET packages exactly: the Benzene topic travels as an application property
// named per wire.ReservedNames (default "topic", wire-contracts.md §2), headers are the remaining
// string-typed application properties, and the message bytes are the Service Bus message Body.
package azureservicebus

import (
	"context"
	"encoding/json"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// SenderAPI is the single Service Bus SDK method Client depends on - sending one message to the
// queue or topic the sender was created for. Depending on this narrow interface, rather than the
// concrete *azservicebus.Sender, makes Client testable with a fake - no live Service Bus needed.
// *azservicebus.Sender satisfies it as-is (its SendMessage has exactly this signature).
type SenderAPI interface {
	SendMessage(ctx context.Context, message *azservicebus.Message, options *azservicebus.SendMessageOptions) error
}

// Client publishes outbound Benzene messages to a Service Bus queue or topic via a caller-supplied
// sender (obtained from azservicebus.Client.NewSender(queueOrTopicName)). It satisfies client.Sender,
// so it composes with client.WithCorrelationID/WithRetry like any other Sender.
//
// Like the .NET ServiceBusBenzeneMessageClient, it wraps an already-built sender rather than a
// connection string or credential: the caller builds the azservicebus.Client however they choose
// (connection string, DefaultAzureCredential for Managed Identity, the emulator) and Benzene never
// wraps that choice.
type Client struct {
	API SenderAPI

	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2). Leave the zero
	// value for the defaults; set it to the SAME value the consuming Worker configured, so the topic
	// application property this client writes is the one that side reads.
	ReservedNames wire.ReservedNames
}

// NewClient returns a Client publishing via api (typically an *azservicebus.Sender bound to a queue
// or topic).
func NewClient(api SenderAPI) *Client {
	return &Client{API: api}
}

// Send publishes message to the sender's entity, with topic written as the reserved topic
// application property (default "topic", ReservedNames-configurable) per wire-contracts.md §2 and
// headers written as additional application properties - the same properties the consuming Worker
// reads to route and rehydrate headers. The message bytes become the Service Bus message Body.
//
// Service Bus has no request/response beyond a send acknowledgement, so a successful send maps to
// StatusAccepted ("Accepted for asynchronous processing"), matching the .NET client. A
// transport-level failure maps to ServiceUnavailable, like every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	properties := make(map[string]any, len(headers)+1)
	for k, v := range headers {
		properties[k] = v
	}
	// The topic property is set after the headers so it wins over a stray header of the same name,
	// matching the .NET converter's ordering (it assigns the topic property last).
	properties[c.ReservedNames.Topic()] = topic.String()

	msg := &azservicebus.Message{
		Body:                  message,
		ApplicationProperties: properties,
	}
	if err := c.API.SendMessage(ctx, msg, nil); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("azureservicebus: send failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
