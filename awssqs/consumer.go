package awssqs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// ReceiveDeleteAPI is the narrow slice of the SQS SDK the Consumer depends on - long-poll receive
// plus batch delete. Depending on this interface (not the concrete *sqs.Client) keeps the Consumer
// testable with a fake, no SigV4 or live AWS needed; *sqs.Client satisfies it as-is. It is the
// self-hosted counterpart of the Lambda-trigger Handler: the same topic/body/header resolution, but
// this owns the ReceiveMessage/DeleteMessageBatch loop the Lambda event source mapping otherwise runs.
type ReceiveDeleteAPI interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(ctx context.Context, params *sqs.DeleteMessageBatchInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// Consumer is a self-hosted SQS poller (Benzene.Aws.Sqs's SqsConsumer): it long-polls a queue and
// runs each received message through the pipeline in its own DI scope, deleting only the messages
// whose dispatch succeeded. A message whose dispatch is unsuccessful is left on the queue, so it
// reappears after its visibility timeout and SQS's own redrive policy (maxReceiveCount -> DLQ)
// eventually takes over - this consumer never deletes an unhandled message. It is the standalone
// alternative to the Lambda-trigger Handler for a service that owns its own compute (a container,
// an EC2 worker) rather than being invoked by an event source mapping.
type Consumer struct {
	// API is the SQS client (typically an *sqs.Client). Required.
	API ReceiveDeleteAPI
	// QueueURL is the queue to poll. Required.
	QueueURL string
	// Builder is the application whose pipeline each message dispatches through. Required.
	Builder *benzene.ApplicationBuilder

	// MaxMessages is how many messages one ReceiveMessage call may return (1-10, SQS's cap).
	// Default 10 when <= 0.
	MaxMessages int32
	// WaitTimeSeconds is the long-poll wait (1-20). Long polling is cheaper and lower-latency than
	// busy short polling and (because ReceiveMessage then blocks server-side) is what throttles
	// Run's loop. Default 20 when <= 0; leave it zero for the default. (Short polling - a literal
	// 0 - is not settable via this field on purpose: on an idle queue it spins Run at high CPU.)
	WaitTimeSeconds int32
	// VisibilityTimeout is the per-receive visibility timeout in seconds (0 uses the queue's
	// configured default). It must exceed the handler's worst-case processing time, or SQS will
	// redeliver a message that is still being handled.
	VisibilityTimeout int32
	// ErrorBackoff is how long Run waits after a ReceiveMessage/DeleteMessageBatch error before
	// polling again, so a transient AWS outage doesn't become a hot error loop. Default 1s when 0.
	ErrorBackoff time.Duration
	// ReservedNames overrides the reserved metadata names (wire-contracts.md §2) used to read the
	// topic attribute. Leave it zero to inherit the Builder's reserved names (so topic resolution
	// matches the sibling Lambda Handler out of the box); set it only to differ from the Builder.
	ReservedNames wire.ReservedNames
	// OnFailure, when non-nil, is called for each message whose dispatch was unsuccessful (the
	// message is left on the queue for redelivery regardless). Use it to log or emit a metric; SQS
	// has no broker-side nack, so a failure is "not deleted", not an explicit reject.
	OnFailure func(messageID string, response wire.Response)

	// sleep is the backoff primitive, injectable for tests. nil means sleepContext.
	sleep func(ctx context.Context, d time.Duration)
}

// errMissingDeps guards the required fields with a clear message rather than a nil dereference deep
// in the poll loop.
var errMissingDeps = errors.New("awssqs: Consumer requires API, QueueURL, and Builder")

// Validate reports whether the Consumer is runnable - call it at startup for a clear error instead
// of a panic from Run's first poll.
func (c *Consumer) Validate() error {
	if c.API == nil || c.QueueURL == "" || c.Builder == nil {
		return errMissingDeps
	}
	return nil
}

func (c *Consumer) maxMessages() int32 {
	if c.MaxMessages <= 0 {
		return 10
	}
	return c.MaxMessages
}

func (c *Consumer) waitTime() int32 {
	if c.WaitTimeSeconds <= 0 {
		return 20
	}
	return c.WaitTimeSeconds
}

func (c *Consumer) errorBackoff() time.Duration {
	if c.ErrorBackoff == 0 {
		return time.Second
	}
	return c.ErrorBackoff
}

// topicKey resolves the reserved topic attribute key: the Consumer's explicit override if set, else
// the Builder's reserved names (which default to "topic"), so topic resolution matches the sibling
// Lambda Handler out of the box.
func (c *Consumer) topicKey() string {
	if c.ReservedNames.TopicKey != "" {
		return c.ReservedNames.TopicKey
	}
	return c.Builder.ReservedNames.Topic()
}

func (c *Consumer) sleepFor(ctx context.Context, d time.Duration) {
	if c.sleep != nil {
		c.sleep(ctx, d)
		return
	}
	sleepContext(ctx, d)
}

// Run polls and dispatches until ctx is cancelled, then returns ctx.Err(). A receive or delete
// error is not fatal: Run backs off (ErrorBackoff) and keeps polling, so the loop survives a
// transient AWS outage. Run one Consumer per goroutine; SQS's at-least-once delivery means handlers
// must be idempotent. Call Validate first.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.poll(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.sleepFor(ctx, c.errorBackoff())
		}
	}
}

// poll runs one receive/dispatch/delete cycle. It is exported-in-spirit for tests (a single
// deterministic cycle) while Run wraps it in the lifecycle loop.
func (c *Consumer) poll(ctx context.Context) error {
	out, err := c.API.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(c.QueueURL),
		MaxNumberOfMessages:   c.maxMessages(),
		WaitTimeSeconds:       c.waitTime(),
		VisibilityTimeout:     c.VisibilityTimeout,
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return err
	}

	var toDelete []types.DeleteMessageBatchRequestEntry
	for _, message := range out.Messages {
		req := resolveMessage(message, c.topicKey())
		response, successful := envelope.DispatchResult(ctx, c.Builder.Pipeline, c.Builder.Container, req)
		if successful {
			toDelete = append(toDelete, types.DeleteMessageBatchRequestEntry{
				Id:            message.MessageId,
				ReceiptHandle: message.ReceiptHandle,
			})
			continue
		}
		if c.OnFailure != nil {
			c.OnFailure(aws.ToString(message.MessageId), response)
		}
	}

	if len(toDelete) == 0 {
		return nil
	}
	// Delete on a cancellation-detached context so a graceful shutdown (ctx cancelled after a message
	// was successfully handled) still acks the completed work, rather than cancelling the delete and
	// letting SQS redeliver already-processed messages - the same settlement-outlives-cancellation
	// choice the idempotency package makes. Handlers must be idempotent regardless (at-least-once).
	deleteOut, err := c.API.DeleteMessageBatch(context.WithoutCancel(ctx), &sqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(c.QueueURL),
		Entries:  toDelete,
	})
	if err != nil {
		return err
	}
	// A partial batch-delete failure (HTTP 200 with entries in Failed) means those messages were NOT
	// removed server-side and will redeliver - safe (at-least-once), never a drop. Surface each via the
	// OnFailure hook so the redelivery is observable rather than silent.
	if c.OnFailure != nil {
		for _, f := range deleteOut.Failed {
			c.OnFailure(aws.ToString(f.Id), wire.Response{StatusCode: string(benzene.StatusServiceUnavailable)})
		}
	}
	return nil
}

// resolveMessage resolves an SQS SDK message into a wire.Request the same way the Lambda-trigger
// Handler resolves an event-source-mapping record: the topic from the reserved topic attribute
// (wire-contracts.md §2), other attributes as headers, else the body parsed as a full envelope.
func resolveMessage(message types.Message, topicKey string) wire.Request {
	metadata := make(map[string]string, len(message.MessageAttributes))
	for name, attr := range message.MessageAttributes {
		metadata[name] = aws.ToString(attr.StringValue)
	}
	topic, headers := wire.ResolveMetadataTopic(metadata, topicKey)
	body := aws.ToString(message.Body)

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: body}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal([]byte(body), &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}
	return wire.Request{Topic: "", Headers: headers, Body: body}
}

// sleepContext sleeps for d or until ctx is cancelled, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
