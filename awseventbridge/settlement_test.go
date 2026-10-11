package awseventbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/daniellepelley/benzene-go/envelope"
)

func TestHandler_Settlement(t *testing.T) {
	failed := json.RawMessage(`{"id":"e-1","detail-type":"greet","source":"s","detail":{"name":""}}`)
	unmatched := json.RawMessage(`{"id":"e-2","detail-type":"someone:elses","source":"s","detail":{}}`)

	atMostOnce := Handler(newTestBuilder(t), WithSettlement(envelope.Settlement{Delivery: envelope.AtMostOnce}))
	if _, err := atMostOnce(context.Background(), failed); err != nil {
		t.Errorf("at-most-once error = %v, want nil whatever the outcome", err)
	}

	ignoring := Handler(newTestBuilder(t), WithSettlement(envelope.Settlement{IgnoreUnmatchedTopics: true}))
	if _, err := ignoring(context.Background(), unmatched); err != nil {
		t.Errorf("unmatched detail-type with IgnoreUnmatchedTopics error = %v, want nil", err)
	}
	if _, err := ignoring(context.Background(), failed); err == nil {
		t.Error("a handler failure with IgnoreUnmatchedTopics error = nil, want the invocation failed")
	}
}
