package gcppubsubclient

import (
	"context"

	"cloud.google.com/go/pubsub"
)

// topicPublisher adapts a single *pubsub.Topic to the Publisher interface. It is the one place
// this module touches the cloud.google.com/go/pubsub SDK: a thin bridge from the narrow Publisher
// contract to the real Topic.Publish/PublishResult.Get API, so all testable logic lives in Client
// behind the interface (this wrapper is exercised only against a live Pub/Sub topic, not in unit
// tests).
type topicPublisher struct {
	topic *pubsub.Topic
}

// NewTopicPublisher adapts a cloud.google.com/go/pubsub *Topic to a Publisher, ready to pass to
// NewClient. Build the topic from your own client, e.g.:
//
//	psClient, err := pubsub.NewClient(ctx, projectID) // returns *pubsub.Client
//	// ... handle err ...
//	defer psClient.Close()                            // the client's lifecycle is the app's
//	topic := psClient.Topic("my-topic")
//	defer topic.Stop()                                // flush + release the publish bundler
//	c := gcppubsubclient.NewClient(gcppubsubclient.NewTopicPublisher(topic), "my-topic")
//
// Ownership of the *pubsub.Client and *pubsub.Topic - including Close() on the client and Stop()
// on the topic - stays with the caller; this module never creates or tears down either.
func NewTopicPublisher(topic *pubsub.Topic) Publisher {
	return &topicPublisher{topic: topic}
}

// Publish sends one message to the fixed *pubsub.Topic and blocks for the server-assigned id. The
// physical topic is the handle supplied to NewTopicPublisher, so the topicID argument (the
// Client's configured id, used by fakes for assertions) is not re-resolved here.
func (p *topicPublisher) Publish(ctx context.Context, _ string, data []byte, attributes map[string]string) (string, error) {
	result := p.topic.Publish(ctx, &pubsub.Message{Data: data, Attributes: attributes})
	return result.Get(ctx)
}
