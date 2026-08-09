// Package gcppubsubclient is the outbound half of the Google Cloud Pub/Sub binding: the publish
// client that pairs with the inbound push-subscription handler in
// github.com/daniellepelley/benzene-go/gcppubsub. It is the Go port of
// Benzene.Clients.GoogleCloud.PubSub.
//
// It lives in its own Go module because publishing needs the cloud.google.com/go/pubsub SDK
// (Pub/Sub's Publish API is OAuth-signed gRPC, not a plain HTTPS POST the way the inbound push
// delivery is), and this repo keeps every third-party dependency isolated in its own module so
// the zero-dependency root module stays zero-dependency - the same rule the awssqs/awssns
// outbound clients follow. See RELEASING.md.
//
// The publish contract mirrors the inbound handler and wire-contracts.md §2: the message payload
// is the body, and the Benzene routing topic travels as a Pub/Sub message ATTRIBUTE named per
// ReservedNames.Topic() (default "topic"), alongside the message headers as further attributes.
// The physical Pub/Sub topic a Client publishes to is a separate, fixed piece of configuration -
// on Pub/Sub the routing topic and the physical topic are independent, exactly as on the inbound
// side (which routes on the attribute, not the Pub/Sub topic name).
//
// Client satisfies client.Sender, so it composes with client.WithCorrelationID/WithRetry
// like any other Sender. Publishing is fire-and-acknowledge: a successful publish returns a
// server-assigned message id (no payload), so a success maps to StatusAccepted, matching every
// other queue-shaped Sender in this repo; a publish failure maps to ServiceUnavailable.
package gcppubsubclient

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// Publisher is the single Pub/Sub operation Client depends on: publish data with attributes to a
// physical Pub/Sub topic and block for the server-assigned message id. Depending on this narrow
// interface - rather than the concrete cloud.google.com/go/pubsub types - keeps Client fully
// testable with a fake, so unit tests need no live Pub/Sub and never import the heavy SDK.
//
// topicID identifies the physical Pub/Sub topic (the value passed to NewClient); it is threaded
// through so a fake can assert on it. NewTopicPublisher is the real-SDK adapter.
type Publisher interface {
	Publish(ctx context.Context, topicID string, data []byte, attributes map[string]string) (serverID string, err error)
}

// Client publishes outbound Benzene messages to a Google Cloud Pub/Sub topic. It satisfies
// client.Sender.
type Client struct {
	// Publisher performs the actual publish. In production it is the NewTopicPublisher adapter
	// over a cloud.google.com/go/pubsub client; in tests it is a fake.
	Publisher Publisher
	// TopicID is the physical Pub/Sub topic messages are published to. It is passed to
	// Publisher.Publish and is distinct from the Benzene routing topic (which travels as the
	// reserved topic attribute).
	TopicID string
	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2). Leave the zero
	// value for the defaults; set it to the SAME value the consuming service configured on its
	// inbound bindings (an override applies to both directions), so the topic attribute this
	// client writes is the one that side reads.
	ReservedNames wire.ReservedNames
}

// NewClient returns a Client publishing to the Pub/Sub topic identified by topicID via publisher.
// Use NewTopicPublisher to build publisher from a real cloud.google.com/go/pubsub topic.
func NewClient(publisher Publisher, topicID string) *Client {
	return &Client{Publisher: publisher, TopicID: topicID}
}

// Send publishes message to the configured Pub/Sub topic, with the Benzene routing topic written
// as the reserved topic attribute (default "topic", ReservedNames-configurable) per
// wire-contracts.md §2 and headers written as additional message attributes - the same shape the
// inbound gcppubsub handler reads. It blocks for the server-assigned message id (via the
// PublishResult the SDK returns).
//
// A successful publish maps to StatusAccepted - wire-contracts.md §3's own description fits
// exactly: "Accepted for asynchronous processing". A publish failure maps to ServiceUnavailable,
// matching every other Sender in this repo. An empty header value is dropped rather than written
// as an empty attribute, mirroring the .NET converter.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	attributes := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		if v != "" {
			attributes[k] = v
		}
	}
	attributes[c.ReservedNames.Topic()] = topic.String()

	if _, err := c.Publisher.Publish(ctx, c.TopicID, message, attributes); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("gcppubsubclient: publish failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
