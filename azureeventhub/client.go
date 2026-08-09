package azureeventhub

import (
	"context"
	"encoding/json"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs/v2"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// ProducerAPI is the single Event Hubs producer operation Client depends on: send one event
// (its body bytes and its string->any properties, with an optional partition key). Depending on
// this narrow interface, rather than the concrete *azeventhubs.ProducerClient, keeps Client
// testable with a fake - no live Azure, and no SDK import from the test. NewProducerAdapter wraps
// a real *azeventhubs.ProducerClient to satisfy it (batch of one: NewEventDataBatch -> AddEventData
// -> SendEventDataBatch, the SDK's only send path).
type ProducerAPI interface {
	SendEvent(ctx context.Context, body []byte, properties map[string]any, partitionKey *string) error
}

// Client publishes outbound Benzene messages to an Event Hub. It satisfies client.Sender, so it
// can be wrapped in client.CorrelationDecorator/RetryDecorator like any other Sender. It mirrors
// Benzene.Clients.Azure.EventHub.EventHubBenzeneMessageClient: the topic and headers travel as
// event properties, the message bytes as the event body, and a completed send is an acknowledgement
// only (Event Hubs has no request/response semantics).
type Client struct {
	Producer ProducerAPI

	// PartitionKeyHeader, when non-empty, names the request header whose value becomes the event's
	// partition key - co-locating related events on one partition, preserving their order there
	// (matching the .NET converter's partitionKeyHeader). The header is still also written as an
	// event property. Empty (the default) sends with no partition key, letting Event Hubs distribute
	// events across partitions.
	PartitionKeyHeader string

	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2). Leave the zero
	// value for the defaults; set it to the SAME value the consuming service configured (an override
	// applies to both directions), so the topic property this client writes is the one that side
	// reads.
	ReservedNames wire.ReservedNames
}

// NewClient returns a Client publishing via producer (typically NewProducerAdapter over an
// *azeventhubs.ProducerClient constructed from your own connection string / credential).
func NewClient(producer ProducerAPI) *Client {
	return &Client{Producer: producer}
}

// Send publishes message to the Event Hub, with topic written as the reserved topic event property
// (default "topic", ReservedNames-configurable) and headers written as additional event properties,
// matching Benzene.Clients.Azure.EventHub. A successful send maps to StatusAccepted
// (wire-contracts.md §3, "Accepted for asynchronous processing"); a transport-level failure maps to
// ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	properties := make(map[string]any, len(headers)+1)
	for k, v := range headers {
		properties[k] = v
	}
	properties[c.ReservedNames.Topic()] = topic.String()

	var partitionKey *string
	if c.PartitionKeyHeader != "" {
		if v, ok := headers[c.PartitionKeyHeader]; ok {
			partitionKey = &v
		}
	}

	if err := c.Producer.SendEvent(ctx, message, properties, partitionKey); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("azureeventhub: send failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

// producerAdapter bridges a real *azeventhubs.ProducerClient onto ProducerAPI. The SDK sends only
// via a batch, so a single event is sent as a batch of one - the same shape as
// EventHubClientMiddleware in .NET (CreateBatch -> TryAdd -> Send).
type producerAdapter struct {
	producer *azeventhubs.ProducerClient
}

// NewProducerAdapter wraps producer so a Client can send through it. The caller owns producer's
// authentication and lifetime (Close it on shutdown). This is a thin pass-through to the real SDK;
// this package's tests exercise Client against a ProducerAPI fake instead, so the adapter itself is
// covered only by a live Event Hub.
func NewProducerAdapter(producer *azeventhubs.ProducerClient) ProducerAPI {
	return producerAdapter{producer: producer}
}

func (a producerAdapter) SendEvent(ctx context.Context, body []byte, properties map[string]any, partitionKey *string) error {
	opts := &azeventhubs.EventDataBatchOptions{}
	if partitionKey != nil {
		opts.PartitionKey = partitionKey
	}
	batch, err := a.producer.NewEventDataBatch(ctx, opts)
	if err != nil {
		return err
	}
	if err := batch.AddEventData(&azeventhubs.EventData{Body: body, Properties: properties}, nil); err != nil {
		return err
	}
	return a.producer.SendEventDataBatch(ctx, batch, nil)
}

var _ client.Sender = (*Client)(nil)
