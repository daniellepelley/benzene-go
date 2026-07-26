package awseventbridge

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
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

// Send publishes one entry: DetailType is the topic, verbatim (matching Handler's inbound
// mapping, so rules can pattern-match per Benzene topic), and Detail is message with any
// headers embedded under EmbeddedHeadersKey (see embedDetailHeaders) - EventBridge has no
// native per-message attributes, so that's the only channel wire headers can travel on;
// Handler lifts them back out on the consuming side. A successful publish maps to
// StatusAccepted; a transport-level failure - a Go error or a non-zero FailedEntryCount,
// which PutEvents reports per entry without an error - maps to ServiceUnavailable, matching
// every other Sender in this repo.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	entry := types.PutEventsRequestEntry{
		Source:     aws.String(c.Source),
		DetailType: aws.String(topic.String()),
		Detail:     aws.String(embedDetailHeaders(message, headers)),
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

// embedDetailHeaders embeds headers into message under EmbeddedHeadersKey, matching the main
// repo's EventBridgeContextConverter.BuildDetail exactly: embedding only happens when there
// are headers to send and message parses as a JSON object (an extra field is benign for
// non-Benzene consumers, but there is nowhere for a header object to live inside a JSON
// array/scalar). Otherwise message is returned verbatim - dropping headers on a non-object
// payload is deliberate, matching the reference implementation's behavior, not this port's
// invention.
func embedDetailHeaders(message []byte, headers map[string]string) string {
	if len(headers) == 0 {
		return string(message)
	}

	var detail map[string]json.RawMessage
	if err := json.Unmarshal(message, &detail); err != nil {
		return string(message)
	}

	embedded := make(map[string]string, len(headers))
	for k, v := range headers {
		embedded[k] = v
	}
	encodedHeaders, err := json.Marshal(embedded)
	if err != nil {
		// embedded is a plain string map - Marshal cannot fail on it in practice, but degrade
		// to the unmodified message rather than panic if it somehow ever does.
		return string(message)
	}
	detail[EmbeddedHeadersKey] = encodedHeaders

	encodedDetail, err := json.Marshal(detail)
	if err != nil {
		// detail's values are either the original message's own already-valid json.RawMessage
		// fields or encodedHeaders (just marshaled successfully above) - Marshal cannot fail
		// here in practice, but degrade rather than panic if it somehow ever does.
		return string(message)
	}
	return string(encodedDetail)
}

var _ client.Sender = (*Client)(nil)
