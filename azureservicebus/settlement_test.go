package azureservicebus

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

func TestWorker_Settlement(t *testing.T) {
	batch := func() *fakeReceiver {
		return &fakeReceiver{batches: [][]*azservicebus.ReceivedMessage{{
			received("failed", "greet", `{"name":""}`),
			received("unmatched", "someone:elses", `{}`),
		}}}
	}

	api := batch()
	reported := 0
	w := &Worker{API: api, Builder: newTestBuilder(t), AckMode: AckModeDeadLetter,
		Settlement: envelope.Settlement{Delivery: envelope.AtMostOnce},
		OnFailure:  func(string, wire.Response) { reported++ }}
	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.completed) != 2 || len(api.abandoned)+len(api.deadLettered) != 0 || reported != 2 {
		t.Errorf("at-most-once completed %v abandoned %v dead %v reported %d, want both completed and both reported",
			api.completed, api.abandoned, api.deadLettered, reported)
	}

	api = batch()
	w = &Worker{API: api, Builder: newTestBuilder(t), Settlement: envelope.Settlement{IgnoreUnmatchedTopics: true}}
	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll() error = %v", err)
	}
	if len(api.completed) != 1 || api.completed[0] != "unmatched" || len(api.abandoned) != 1 || api.abandoned[0] != "failed" {
		t.Errorf("IgnoreUnmatchedTopics completed %v abandoned %v, want [unmatched] completed and [failed] abandoned", api.completed, api.abandoned)
	}
}
