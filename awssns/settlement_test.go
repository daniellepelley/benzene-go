package awssns

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/daniellepelley/benzene-go/envelope"
)

func TestHandler_Settlement(t *testing.T) {
	failed := json.RawMessage(`{"Records":[{"Sns":{"MessageId":"msg-1","Message":"{\"name\":\"\"}","MessageAttributes":{"topic":{"Value":"greet"}}}}]}`)
	unmatched := json.RawMessage(`{"Records":[{"Sns":{"MessageId":"msg-2","Message":"{}","MessageAttributes":{"topic":{"Value":"someone:elses"}}}}]}`)

	atMostOnce := Handler(newTestBuilder(t), WithSettlement(envelope.Settlement{Delivery: envelope.AtMostOnce}))
	if _, err := atMostOnce(context.Background(), failed); err != nil {
		t.Errorf("at-most-once error = %v, want nil whatever the outcome", err)
	}

	ignoring := Handler(newTestBuilder(t), WithSettlement(envelope.Settlement{IgnoreUnmatchedTopics: true}))
	if _, err := ignoring(context.Background(), unmatched); err != nil {
		t.Errorf("unmatched topic with IgnoreUnmatchedTopics error = %v, want nil", err)
	}
	if _, err := ignoring(context.Background(), failed); err == nil {
		t.Error("a handler failure with IgnoreUnmatchedTopics error = nil, want the invocation failed")
	}
}
