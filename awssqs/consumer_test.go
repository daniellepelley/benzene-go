package awssqs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// fakeSQS is an in-memory ReceiveDeleteAPI: it hands out one scripted batch per ReceiveMessage call
// and records deleted message IDs, with injectable receive/delete errors and a per-call hook.
type fakeSQS struct {
	batches     [][]types.Message
	recvErr     error
	delErr      error
	failDelete  []string // message IDs the server reports in out.Failed (not actually deleted)
	recvCalls   int
	deleteCalls int
	deletedIDs  []string
	onReceive   func(call int)
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	call := f.recvCalls
	f.recvCalls++
	if f.onReceive != nil {
		f.onReceive(call)
	}
	if f.recvErr != nil {
		return nil, f.recvErr
	}
	var msgs []types.Message
	if call < len(f.batches) {
		msgs = f.batches[call]
	}
	return &sqs.ReceiveMessageOutput{Messages: msgs}, nil
}

func (f *fakeSQS) DeleteMessageBatch(_ context.Context, in *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	f.deleteCalls++
	if f.delErr != nil {
		return nil, f.delErr
	}
	failing := map[string]bool{}
	for _, id := range f.failDelete {
		failing[id] = true
	}
	out := &sqs.DeleteMessageBatchOutput{}
	for _, e := range in.Entries {
		id := aws.ToString(e.Id)
		if failing[id] {
			out.Failed = append(out.Failed, types.BatchResultErrorEntry{Id: e.Id})
			continue
		}
		f.deletedIDs = append(f.deletedIDs, id)
	}
	return out, nil
}

func message(id, topic, body string) types.Message {
	m := types.Message{
		MessageId:     aws.String(id),
		ReceiptHandle: aws.String("rh-" + id),
		Body:          aws.String(body),
	}
	if topic != "" {
		m.MessageAttributes = map[string]types.MessageAttributeValue{
			"topic": {DataType: aws.String("String"), StringValue: aws.String(topic)},
		}
	}
	return m
}

func TestConsumer_PollDispatchesAndDeletesSuccessful(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{message("m-1", "greet", `{"name":"World"}`)}}}
	c := NewConsumer(api, "q", newTestBuilder(t))

	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 1 || api.deletedIDs[0] != "m-1" {
		t.Errorf("deleted = %v, want [m-1] (successful dispatch is deleted)", api.deletedIDs)
	}
}

func TestConsumer_PollLeavesFailedMessageAndReportsIt(t *testing.T) {
	var gotID string
	var gotStatus string
	api := &fakeSQS{batches: [][]types.Message{{message("m-2", "greet", `{"name":""}`)}}} // empty name -> BadRequest
	c := NewConsumer(api, "q", newTestBuilder(t),
		WithOnFailure(func(id string, resp wire.Response) { gotID, gotStatus = id, resp.StatusCode }))

	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 0 {
		t.Errorf("deleted = %v, want none (a failed message must not be deleted)", api.deletedIDs)
	}
	if api.deleteCalls != 0 {
		t.Errorf("DeleteMessageBatch called %d times, want 0 when nothing succeeded", api.deleteCalls)
	}
	if gotID != "m-2" || gotStatus != string(benzene.StatusBadRequest) {
		t.Errorf("OnFailure got (%q, %q), want (m-2, bad-request)", gotID, gotStatus)
	}
}

func TestConsumer_PollMixedBatchDeletesOnlySuccessful(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{
		message("ok-1", "greet", `{"name":"A"}`),
		message("bad-1", "greet", `{"name":""}`),
		message("ok-2", "greet", `{"name":"B"}`),
	}}}
	c := NewConsumer(api, "q", newTestBuilder(t))

	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 2 || api.deletedIDs[0] != "ok-1" || api.deletedIDs[1] != "ok-2" {
		t.Errorf("deleted = %v, want [ok-1 ok-2] (bad-1 left for redelivery)", api.deletedIDs)
	}
}

func TestConsumer_PollEnvelopeBodyFallback(t *testing.T) {
	// No topic attribute: the body is parsed as a full wire envelope.
	envelope := `{"topic":"greet","headers":{"correlation-id":"abc"},"body":"{\"name\":\"Env\"}"}`
	api := &fakeSQS{batches: [][]types.Message{{message("m-3", "", envelope)}}}
	c := NewConsumer(api, "q", newTestBuilder(t))

	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 1 || api.deletedIDs[0] != "m-3" {
		t.Errorf("deleted = %v, want [m-3] (envelope body resolved the topic)", api.deletedIDs)
	}
}

func TestConsumer_PollNoMessagesDoesNotDelete(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{}}, delErr: errors.New("delete must not be called")}
	c := NewConsumer(api, "q", newTestBuilder(t))

	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v, want nil when the batch is empty", err)
	}
	if api.deleteCalls != 0 {
		t.Errorf("DeleteMessageBatch called %d times, want 0 for an empty receive", api.deleteCalls)
	}
}

func TestConsumer_PollReceiveErrorPropagates(t *testing.T) {
	wantErr := errors.New("receive boom")
	c := NewConsumer(&fakeSQS{recvErr: wantErr}, "q", newTestBuilder(t))
	if err := c.poll(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("poll() error = %v, want %v", err, wantErr)
	}
}

func TestConsumer_PollDeleteErrorPropagates(t *testing.T) {
	wantErr := errors.New("delete boom")
	api := &fakeSQS{batches: [][]types.Message{{message("m-4", "greet", `{"name":"X"}`)}}, delErr: wantErr}
	c := NewConsumer(api, "q", newTestBuilder(t))
	if err := c.poll(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("poll() error = %v, want %v", err, wantErr)
	}
}

func TestConsumer_RunStopsWhenContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewConsumer(&fakeSQS{}, "q", newTestBuilder(t))
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want context.Canceled", err)
	}
}

func TestConsumer_RunProcessesABatchThenStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeSQS{
		batches:   [][]types.Message{{message("run-1", "greet", `{"name":"Loop"}`)}},
		onReceive: func(call int) {}, // set below
	}
	// Cancel after the first receive so Run processes one batch then exits at the loop top.
	api.onReceive = func(call int) {
		if call == 0 {
			// let this receive return the batch; cancel so the next iteration's ctx.Err() stops Run
			cancel()
		}
	}
	c := NewConsumer(api, "q", newTestBuilder(t))
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want context.Canceled", err)
	}
	if len(api.deletedIDs) != 1 || api.deletedIDs[0] != "run-1" {
		t.Errorf("deleted = %v, want [run-1] processed before Run stopped", api.deletedIDs)
	}
}

func TestConsumer_RunBacksOffAfterErrorThenStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeSQS{recvErr: errors.New("transient")}
	c := NewConsumer(api, "q", newTestBuilder(t), WithErrorBackoff(time.Hour))
	// Replace the backoff sleep so the test doesn't actually wait an hour; cancel to end Run.
	c.sleep = func(context.Context, time.Duration) { cancel() }

	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want context.Canceled after backing off", err)
	}
	if api.recvCalls == 0 {
		t.Error("expected at least one ReceiveMessage attempt before backoff")
	}
}

func TestConsumer_RunReturnsContextErrorWhenCancelledDuringReceiveError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeSQS{recvErr: errors.New("boom"), onReceive: func(int) { cancel() }}
	c := NewConsumer(api, "q", newTestBuilder(t))
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v, want context.Canceled (cancelled during a failing receive)", err)
	}
}

func TestConsumer_PollUnresolvableTopicIsNotDeleted(t *testing.T) {
	// No topic attribute and a body that is not a wire envelope: the request carries an empty topic,
	// which RouterMiddleware turns into a failure - the message is reported and left, never deleted.
	api := &fakeSQS{batches: [][]types.Message{{message("m-5", "", "plain text, not an envelope")}}}
	c := NewConsumer(api, "q", newTestBuilder(t))
	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 0 {
		t.Errorf("deleted = %v, want none for an unresolvable message", api.deletedIDs)
	}
}

func TestSleepContext(t *testing.T) {
	// Elapses normally.
	start := time.Now()
	sleepContext(context.Background(), time.Millisecond)
	if time.Since(start) < time.Millisecond {
		t.Error("sleepContext returned before the duration elapsed")
	}
	// Returns promptly when the context is already cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { sleepContext(ctx, time.Hour); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("sleepContext did not return promptly on a cancelled context")
	}
}

func TestConsumer_HonorsBuilderReservedNames(t *testing.T) {
	// A service that overrode the topic key on its builder must get the same key in the Consumer,
	// without WithConsumerReservedNames - otherwise every message resolves to an empty topic.
	builder := newTestBuilder(t)
	builder.UseReservedNames(wire.ReservedNames{TopicKey: "x-topic"})

	// The message carries the topic under the CUSTOM key; the default "topic" key is absent.
	m := types.Message{
		MessageId:     aws.String("rn-1"),
		ReceiptHandle: aws.String("rh"),
		Body:          aws.String(`{"name":"Custom"}`),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"x-topic": {DataType: aws.String("String"), StringValue: aws.String("greet")},
		},
	}
	api := &fakeSQS{batches: [][]types.Message{{m}}}
	c := NewConsumer(api, "q", builder)
	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 1 || api.deletedIDs[0] != "rn-1" {
		t.Errorf("deleted = %v, want [rn-1] - the builder's custom topic key must be honored", api.deletedIDs)
	}
}

func TestConsumer_PartialDeleteFailureIsReported(t *testing.T) {
	var reported []string
	api := &fakeSQS{
		batches:    [][]types.Message{{message("d-1", "greet", `{"name":"A"}`)}},
		failDelete: []string{"d-1"}, // server reports d-1 in out.Failed (not actually deleted)
	}
	c := NewConsumer(api, "q", newTestBuilder(t),
		WithOnFailure(func(id string, _ wire.Response) { reported = append(reported, id) }))
	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(reported) != 1 || reported[0] != "d-1" {
		t.Errorf("OnFailure reported %v, want [d-1] for a partial delete failure", reported)
	}
}

func TestConsumer_OptionsAreApplied(t *testing.T) {
	c := NewConsumer(&fakeSQS{}, "q", newTestBuilder(t),
		WithMaxMessages(3),
		WithWaitTimeSeconds(5),
		WithVisibilityTimeout(30),
		WithConsumerReservedNames(wire.ReservedNames{}),
	)
	if c.maxMessages != 3 || c.waitTime != 5 || c.visibilityTimeout != 30 {
		t.Errorf("options not applied: %+v", c)
	}
}
