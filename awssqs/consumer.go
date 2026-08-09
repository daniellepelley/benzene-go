package awssqs

import (
	"context"
	"encoding/json"
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
	api     ReceiveDeleteAPI
	queue   string
	builder *benzene.ApplicationBuilder

	maxMessages       int32
	waitTime          int32
	visibilityTimeout int32
	errorBackoff      time.Duration
	reservedNames     wire.ReservedNames
	onFailure         func(messageID string, response wire.Response)
	sleep             func(ctx context.Context, d time.Duration)
}

// ConsumerOption configures a Consumer.
type ConsumerOption func(*Consumer)

// WithMaxMessages sets how many messages one ReceiveMessage call may return (1-10, SQS's cap).
// Default 10.
func WithMaxMessages(n int32) ConsumerOption { return func(c *Consumer) { c.maxMessages = n } }

// WithWaitTimeSeconds sets the long-poll wait (0-20). A non-zero value is long polling, which is
// cheaper and lower-latency than busy short polling and (because ReceiveMessage then blocks
// server-side) is what throttles Run's loop. Default 20. Setting 0 is short polling: ReceiveMessage
// returns immediately, so on an idle queue Run will spin at high CPU - keep the default unless you
// have a specific reason and your own external pacing.
func WithWaitTimeSeconds(n int32) ConsumerOption { return func(c *Consumer) { c.waitTime = n } }

// WithVisibilityTimeout sets the per-receive visibility timeout in seconds (0 uses the queue's
// configured default). It must exceed the handler's worst-case processing time, or SQS will
// redeliver a message that is still being handled.
func WithVisibilityTimeout(n int32) ConsumerOption {
	return func(c *Consumer) { c.visibilityTimeout = n }
}

// WithErrorBackoff sets how long Run waits after a ReceiveMessage/DeleteMessageBatch error before
// polling again, so a transient AWS outage doesn't become a hot error loop. Default 1s.
func WithErrorBackoff(d time.Duration) ConsumerOption {
	return func(c *Consumer) { c.errorBackoff = d }
}

// WithConsumerReservedNames overrides the reserved metadata names (wire-contracts.md §2) used to
// read the topic attribute. Set it to the SAME value the publisher wrote.
func WithConsumerReservedNames(names wire.ReservedNames) ConsumerOption {
	return func(c *Consumer) { c.reservedNames = names }
}

// WithOnFailure registers a hook called for each message whose dispatch was unsuccessful (the
// message is left on the queue for redelivery regardless). Use it to log or emit a metric; SQS has
// no broker-side nack, so a failure is "not deleted", not an explicit reject.
func WithOnFailure(hook func(messageID string, response wire.Response)) ConsumerOption {
	return func(c *Consumer) { c.onFailure = hook }
}

// NewConsumer builds a Consumer polling queueURL via api (typically an *sqs.Client) and dispatching
// through builder's pipeline.
func NewConsumer(api ReceiveDeleteAPI, queueURL string, builder *benzene.ApplicationBuilder, opts ...ConsumerOption) *Consumer {
	c := &Consumer{
		api:     api,
		queue:   queueURL,
		builder: builder,
		// Default to the builder's reserved names (wire-contracts.md §2 / app.go's UseReservedNames)
		// so topic resolution matches the sibling Lambda Handler out of the box; a service that
		// overrode the topic key on its builder gets the same key here without extra wiring.
		// WithConsumerReservedNames still overrides it explicitly.
		reservedNames: builder.ReservedNames,
		maxMessages:   10,
		waitTime:      20,
		errorBackoff:  time.Second,
		sleep:         sleepContext,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Run polls and dispatches until ctx is cancelled, then returns ctx.Err(). A receive or delete
// error is not fatal: Run backs off (WithErrorBackoff) and keeps polling, so the loop survives a
// transient AWS outage. Run one Consumer per goroutine; SQS's at-least-once delivery means handlers
// must be idempotent.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.poll(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.sleep(ctx, c.errorBackoff)
		}
	}
}

// poll runs one receive/dispatch/delete cycle. It is exported-in-spirit for tests (a single
// deterministic cycle) while Run wraps it in the lifecycle loop.
func (c *Consumer) poll(ctx context.Context) error {
	out, err := c.api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(c.queue),
		MaxNumberOfMessages:   c.maxMessages,
		WaitTimeSeconds:       c.waitTime,
		VisibilityTimeout:     c.visibilityTimeout,
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return err
	}

	var toDelete []types.DeleteMessageBatchRequestEntry
	for _, message := range out.Messages {
		req := resolveMessage(message, c.reservedNames.Topic())
		response, successful := envelope.DispatchResult(ctx, c.builder.Pipeline, c.builder.Container, req)
		if successful {
			toDelete = append(toDelete, types.DeleteMessageBatchRequestEntry{
				Id:            message.MessageId,
				ReceiptHandle: message.ReceiptHandle,
			})
			continue
		}
		if c.onFailure != nil {
			c.onFailure(aws.ToString(message.MessageId), response)
		}
	}

	if len(toDelete) == 0 {
		return nil
	}
	// Delete on a cancellation-detached context so a graceful shutdown (ctx cancelled after a message
	// was successfully handled) still acks the completed work, rather than cancelling the delete and
	// letting SQS redeliver already-processed messages - the same settlement-outlives-cancellation
	// choice the idempotency package makes. Handlers must be idempotent regardless (at-least-once).
	deleteOut, err := c.api.DeleteMessageBatch(context.WithoutCancel(ctx), &sqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(c.queue),
		Entries:  toDelete,
	})
	if err != nil {
		return err
	}
	// A partial batch-delete failure (HTTP 200 with entries in Failed) means those messages were NOT
	// removed server-side and will redeliver - safe (at-least-once), never a drop. Surface each via the
	// OnFailure hook so the redelivery is observable rather than silent.
	if c.onFailure != nil {
		for _, f := range deleteOut.Failed {
			c.onFailure(aws.ToString(f.Id), wire.Response{StatusCode: string(benzene.StatusServiceUnavailable)})
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
