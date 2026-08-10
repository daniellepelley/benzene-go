package responseevents_test

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/responseevents"
)

type order struct {
	ID string `json:"id"`
}

// TestMiddleware_RepublishesThroughHarness dogfoods the response-as-event pattern end to end - the
// copy-worthy shape an adopter wants: boot a real app whose pipeline includes the responseevents
// middleware, push a native event in the front door via the benzenetest harness, and assert the
// follow-up event was published (topic and payload) via a FakeMessageSender. This is the
// ingress -> handler -> egress republish exercised as a whole, not the mapping unit in isolation.
func TestMiddleware_RepublishesThroughHarness(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender()

	app := benzene.App[struct{}]{
		ConfigureServices: func(r *benzene.Registry, _ *benzene.Container, _ struct{}) {
			// order:create returns Created, so CrudConvention republishes it as order:created.
			if err := benzene.Register(r, benzene.NewTopic("order:create"),
				benzene.Handler[order, order](func(_ context.Context, req order) benzene.Result[order] {
					return benzene.Created(req)
				})); err != nil {
				t.Fatalf("Register: %v", err)
			}
		},
		Configure: func(b *benzene.ApplicationBuilder, _ struct{}) {
			publisher := responseevents.NewSenderPublisher(fake)
			middleware := responseevents.Middleware(publisher, []responseevents.Mapping{responseevents.CrudConvention()})
			// The middleware runs before the router so its post-handler block sees the result.
			b.UsePipeline(benzene.NewPipeline(middleware, benzene.RouterMiddleware(b.Registry)))
		},
	}

	host := benzenetest.NewHost(app)

	resp := benzenetest.SendEnvelope(t, host, benzene.NewTopic("order:create"), order{ID: "order-1"}, nil)

	if resp.StatusCode != string(benzene.StatusCreated) {
		t.Fatalf("StatusCode = %q, want %q; body = %s", resp.StatusCode, benzene.StatusCreated, resp.Body)
	}
	if got := fake.LastTopic(); got != benzene.NewTopic("order:created") {
		t.Errorf("published topic = %v, want order:created", got)
	}
	var published order
	fake.DecodeLastMessage(t, &published)
	if published.ID != "order-1" {
		t.Errorf("published ID = %q, want order-1", published.ID)
	}
	if fake.Calls() != 1 {
		t.Errorf("publish Calls = %d, want 1", fake.Calls())
	}
}
