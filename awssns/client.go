package awssns

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// PublishAPI is the single SNS SDK method Client depends on. Depending on this narrow
// interface, rather than the concrete *sns.Client, makes Client testable with a fake - no real
// AWS calls (and no SigV4 mocking) needed in tests. *sns.Client satisfies it as-is.
type PublishAPI interface {
	Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// Client publishes outbound Benzene messages to an SNS topic. It satisfies client.Sender, so it
// can be wrapped in client.CorrelationDecorator/RetryDecorator like any other Sender.
type Client struct {
	API      PublishAPI
	TopicARN string

	// MessageGroupID, when non-nil, computes the FIFO MessageGroupId for a Publish call -
	// required by AWS for FIFO topics (topic ARNs ending in ".fifo"). Leave nil for standard
	// topics.
	MessageGroupID func(topic benzene.Topic) string
	// MessageDeduplicationID, when non-nil, computes the FIFO MessageDeduplicationId for a
	// Publish call. Leave nil to rely on the topic's content-based deduplication setting, if
	// enabled - AWS then derives one from the message body.
	MessageDeduplicationID func(topic benzene.Topic, message []byte) string
}

// NewClient returns a Client publishing to topicARN via api (typically an *sns.Client
// constructed from your own AWS config - see examples/aws-sns-helloworld).
func NewClient(api PublishAPI, topicARN string) *Client {
	return &Client{API: api, TopicARN: topicARN}
}

// Send publishes message to the topic, with topic written as a "topic" message attribute per
// wire-contracts.md §2 ("the topic travels as an attribute named topic") and headers written as
// additional message attributes. A successful publish maps to StatusAccepted - wire-contracts.md
// §3's own description fits exactly: "Accepted for asynchronous processing". A transport-level
// failure maps to ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	attributes := make(map[string]types.MessageAttributeValue, len(headers)+1)
	attributes["topic"] = stringAttribute(topic.String())
	for k, v := range headers {
		attributes[k] = stringAttribute(v)
	}

	input := &sns.PublishInput{
		TopicArn:          aws.String(c.TopicARN),
		Message:           aws.String(string(message)),
		MessageAttributes: attributes,
	}
	if c.MessageGroupID != nil {
		input.MessageGroupId = aws.String(c.MessageGroupID(topic))
	}
	if c.MessageDeduplicationID != nil {
		input.MessageDeduplicationId = aws.String(c.MessageDeduplicationID(topic, message))
	}

	if _, err := c.API.Publish(ctx, input); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("awssns: publish failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

func stringAttribute(value string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
}

var _ client.Sender = (*Client)(nil)
