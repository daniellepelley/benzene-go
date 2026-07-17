package awseventbridge

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// PutEventsAPI is the single EventBridge SDK method Client depends on. Depending on this
// narrow interface, rather than the concrete *eventbridge.Client, makes Client testable with
// a fake - no real AWS calls (and no SigV4 mocking) needed in tests. *eventbridge.Client
// satisfies it as-is.
type PutEventsAPI interface {
	PutEvents(ctx context.Context, params *eventbridge.PutEventsInput, optFns ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

// Client publishes outbound Benzene messages to an EventBridge event bus. It satisfies
// client.Sender, so it can be wrapped in client.CorrelationDecorator/RetryDecorator like any
// other Sender.
type Client struct {
	API PutEventsAPI
	// EventBusName is the target bus; empty publishes to the account's default bus (the SDK's
	// own default).
	EventBusName string
	// Source is the entry's required "source" field - the publishing service's identity
	// (e.g. "com.example.orders"). The caller owns it, exactly like cloudevents.FromRequest's
	// source argument.
	Source string
}

// NewClient returns a Client publishing via api (typically an *eventbridge.Client
// constructed from your own AWS config) with the given source identity.
func NewClient(api PutEventsAPI, source string) *Client {
	return &Client{API: api, Source: source}
}

// Send publishes one entry: detail-type carries the topic (so rules can pattern-match per
// Benzene topic) and detail carries the full wire envelope - the only channel wire headers
// can travel on, unwrapped by Handler on the consuming side (see the package doc). A
// successful publish maps to StatusAccepted; a transport-level failure - a Go error or a
// non-zero FailedEntryCount, which PutEvents reports per entry without an error - maps to
// ServiceUnavailable, matching every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	if headers == nil {
		headers = map[string]string{}
	}
	detail, err := json.Marshal(wire.Request{Topic: topic.String(), Headers: headers, Body: string(message)})
	if err != nil {
		// wire.Request is plain strings and a string map - Marshal cannot fail on it in
		// practice, but degrade to a failed send rather than panic if it somehow ever does.
		return benzene.ServiceUnavailable[json.RawMessage]("awseventbridge: marshal failed: " + err.Error())
	}

	entry := types.PutEventsRequestEntry{
		Source:     aws.String(c.Source),
		DetailType: aws.String(topic.String()),
		Detail:     aws.String(string(detail)),
	}
	if c.EventBusName != "" {
		entry.EventBusName = aws.String(c.EventBusName)
	}

	output, err := c.API.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{entry}})
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("awseventbridge: put failed: " + err.Error())
	}
	if output.FailedEntryCount > 0 {
		detail := "entry rejected"
		if len(output.Entries) > 0 && output.Entries[0].ErrorMessage != nil {
			detail = *output.Entries[0].ErrorMessage
		}
		return benzene.ServiceUnavailable[json.RawMessage]("awseventbridge: put failed: " + detail)
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

var _ client.Sender = (*Client)(nil)
