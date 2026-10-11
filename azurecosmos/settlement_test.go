package azurecosmos

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

func TestWorker_Settlement(t *testing.T) {
	badPage := ChangeFeedPage{Documents: []json.RawMessage{json.RawMessage(`{"id":"x","name":""}`)}, Continuation: "tok-1"}

	var checkpointed []string
	failures := 0
	worker := newRunnableWorker(t, &fakeReader{results: []readResult{{page: badPage}}})
	worker.Settlement = envelope.Settlement{Delivery: envelope.AtMostOnce}
	worker.OnFailure = func(context.Context, ChangeFeedPage, wire.Response) { failures++ }
	worker.Checkpoint = func(_ context.Context, cont string) error { checkpointed = append(checkpointed, cont); return nil }
	if next, backoff := worker.pollAndDispatch(context.Background(), "tok-0"); next != "tok-1" || backoff != 0 {
		t.Errorf("at-most-once = (%q, %v), want (tok-1, 0): skip past the failed batch", next, backoff)
	}
	if failures != 1 || len(checkpointed) != 1 {
		t.Errorf("at-most-once reported %d failures and checkpointed %v, want the failure reported and the batch checkpointed", failures, checkpointed)
	}

	worker = newRunnableWorker(t, &fakeReader{results: []readResult{{page: docsPage("tok-1", "A")}}})
	worker.Topic = benzene.NewTopic("nobody:serves")
	worker.Settlement = envelope.Settlement{IgnoreUnmatchedTopics: true}
	if next, _ := worker.pollAndDispatch(context.Background(), "tok-0"); next != "tok-1" {
		t.Errorf("unmatched code-named topic with IgnoreUnmatchedTopics = %q, want the token advanced", next)
	}
}
