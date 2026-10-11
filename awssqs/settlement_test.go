package awssqs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

func TestHandler_Settlement(t *testing.T) {
	event := `{"Records":[
		{"messageId":"failed","body":"{\"name\":\"\"}","messageAttributes":{"topic":{"stringValue":"greet"}}},
		{"messageId":"unmatched","body":"{}","messageAttributes":{"topic":{"stringValue":"someone:elses"}}}
	]}`
	failures := func(opts ...Option) []string {
		t.Helper()
		out, err := Handler(newTestBuilder(t), opts...)(context.Background(), json.RawMessage(event))
		if err != nil {
			t.Fatalf("handler() error = %v", err)
		}
		var resp batchResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		var ids []string
		for _, f := range resp.BatchItemFailures {
			ids = append(ids, f.ItemIdentifier)
		}
		return ids
	}

	if got := failures(WithSettlement(envelope.Settlement{Delivery: envelope.AtMostOnce})); len(got) != 0 {
		t.Errorf("at-most-once BatchItemFailures = %v, want none", got)
	}
	if got := failures(WithSettlement(envelope.Settlement{IgnoreUnmatchedTopics: true})); len(got) != 1 || got[0] != "failed" {
		t.Errorf("IgnoreUnmatchedTopics BatchItemFailures = %v, want only [failed]", got)
	}
}

func TestConsumer_Settlement(t *testing.T) {
	batch := func() *fakeSQS {
		return &fakeSQS{batches: [][]types.Message{{
			message("failed", "greet", `{"name":""}`),
			message("unmatched", "someone:elses", `{}`),
		}}}
	}

	api := batch()
	var reported []string
	c := &Consumer{API: api, QueueURL: "q", Builder: newTestBuilder(t),
		Settlement: envelope.Settlement{Delivery: envelope.AtMostOnce},
		OnFailure:  func(id string, _ wire.Response) { reported = append(reported, id) }}
	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 2 {
		t.Errorf("at-most-once deleted = %v, want both messages", api.deletedIDs)
	}
	if len(reported) != 2 {
		t.Errorf("OnFailure saw %v, want both failures even though they were deleted", reported)
	}

	api = batch()
	c = &Consumer{API: api, QueueURL: "q", Builder: newTestBuilder(t), Settlement: envelope.Settlement{IgnoreUnmatchedTopics: true}}
	if err := c.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.deletedIDs) != 1 || api.deletedIDs[0] != "unmatched" {
		t.Errorf("IgnoreUnmatchedTopics deleted = %v, want only [unmatched]", api.deletedIDs)
	}
}
