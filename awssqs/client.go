package awssqs

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// SendMessageAPI is the single SQS SDK method Client depends on. Depending on this narrow
// interface, rather than the concrete *sqs.Client, makes Client testable with a fake - no real
// AWS calls (and no SigV4 mocking) needed in tests. *sqs.Client satisfies it as-is.
type SendMessageAPI interface {
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// Client publishes outbound Benzene messages to an SQS queue. It satisfies client.Sender, so it
// can be wrapped in client.CorrelationDecorator/RetryDecorator like any other Sender.
type Client struct {
	API      SendMessageAPI
	QueueURL string

	// MessageGroupID, when non-nil, computes the FIFO MessageGroupId for a Send call -
	// required by AWS for FIFO queues (queue URLs ending in ".fifo"). Leave nil for standard
	// queues.
	MessageGroupID func(topic benzene.Topic) string
	// MessageDeduplicationID, when non-nil, computes the FIFO MessageDeduplicationId for a
	// Send call. Leave nil to rely on the queue's content-based deduplication setting, if
	// enabled - AWS then derives one from the message body.
	MessageDeduplicationID func(topic benzene.Topic, message []byte) string
}

// NewClient returns a Client publishing to queueURL via api (typically an *sqs.Client
// constructed from your own AWS config - see examples/aws-sqs-helloworld).
func NewClient(api SendMessageAPI, queueURL string) *Client {
	return &Client{API: api, QueueURL: queueURL}
}

// Send publishes message to the queue, with topic written as a "topic" message attribute per
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

	input := &sqs.SendMessageInput{
		QueueUrl:          aws.String(c.QueueURL),
		MessageBody:       aws.String(string(message)),
		MessageAttributes: attributes,
	}
	if c.MessageGroupID != nil {
		input.MessageGroupId = aws.String(c.MessageGroupID(topic))
	}
	if c.MessageDeduplicationID != nil {
		input.MessageDeduplicationId = aws.String(c.MessageDeduplicationID(topic, message))
	}

	if _, err := c.API.SendMessage(ctx, input); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("awssqs: send failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

func stringAttribute(value string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
}

var _ client.Sender = (*Client)(nil)
