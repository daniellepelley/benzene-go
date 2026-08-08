package responseevents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// capturingPublisher records every publish and can be told to fail on a given event topic.
type capturingPublisher struct {
	published []Publication
	failOn    map[string]error
}

func (p *capturingPublisher) Publish(_ context.Context, eventTopic benzene.Topic, payload any) error {
	if err, ok := p.failOn[eventTopic.ID]; ok {
		return err
	}
	p.published = append(p.published, Publication{EventTopic: eventTopic, Payload: payload})
	return nil
}

func setResult(ic *benzene.InvocationContext, result benzene.ResultInfo) func(context.Context) error {
	return func(context.Context) error {
		ic.Result = result
		return nil
	}
}

func TestMiddleware_PublishesMatchedEvent(t *testing.T) {
	pub := &capturingPublisher{}
	mw := Middleware(pub, []Mapping{Map("order:create", "order:created")})

	ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:create")}
	order := orderDoc{ID: "o-1"}
	if err := mw(context.Background(), ic, setResult(ic, benzene.CreatedResult(order))); err != nil {
		t.Fatalf("middleware error = %v", err)
	}
	if len(pub.published) != 1 || pub.published[0].EventTopic.ID != "order:created" || pub.published[0].Payload != order {
		t.Fatalf("published = %+v, want one order:created for %+v", pub.published, order)
	}
	// The handler's own result is left untouched on success.
	if ic.Result.ResultStatus() != benzene.StatusCreated {
		t.Errorf("result status = %q, want unchanged 'created'", ic.Result.ResultStatus())
	}
}

func TestMiddleware_FanOutAllMatchingMappings(t *testing.T) {
	pub := &capturingPublisher{}
	mw := Middleware(pub, []Mapping{
		Map("order:create", "order:created"),
		Map("order:create", "audit:order-created"),
	})
	ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:create")}
	if err := mw(context.Background(), ic, setResult(ic, benzene.CreatedResult(orderDoc{ID: "o-1"}))); err != nil {
		t.Fatalf("middleware error = %v", err)
	}
	if len(pub.published) != 2 {
		t.Errorf("published %d events, want 2 (fan-out)", len(pub.published))
	}
}

func TestMiddleware_PassThroughCases(t *testing.T) {
	t.Run("next error propagates and nothing publishes", func(t *testing.T) {
		pub := &capturingPublisher{}
		mw := Middleware(pub, []Mapping{Map("order:create", "order:created")})
		ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:create")}
		wantErr := errors.New("boom")
		if err := mw(context.Background(), ic, func(context.Context) error { return wantErr }); !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		if len(pub.published) != 0 {
			t.Errorf("published %d, want 0 when next errored", len(pub.published))
		}
	})

	t.Run("nil result is a pass-through", func(t *testing.T) {
		pub := &capturingPublisher{}
		mw := Middleware(pub, []Mapping{Map("order:create", "order:created")})
		ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:create")}
		if err := mw(context.Background(), ic, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("middleware error = %v", err)
		}
		if len(pub.published) != 0 {
			t.Errorf("published %d, want 0 for a nil result", len(pub.published))
		}
	})

	t.Run("no mapping matches is a pass-through", func(t *testing.T) {
		pub := &capturingPublisher{}
		mw := Middleware(pub, []Mapping{Map("order:create", "order:created")})
		ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:update")}
		if err := mw(context.Background(), ic, setResult(ic, benzene.Updated(orderDoc{ID: "o-1"}))); err != nil {
			t.Fatalf("middleware error = %v", err)
		}
		if len(pub.published) != 0 {
			t.Errorf("published %d, want 0 when no mapping matches", len(pub.published))
		}
	})
}

func TestMiddleware_FailMessageReplacesResultAndStops(t *testing.T) {
	var hookSource, hookEvent benzene.Topic
	var hookErr error
	pub := &capturingPublisher{failOn: map[string]error{"order:created": errors.New("broker down")}}
	mw := Middleware(pub,
		[]Mapping{
			Map("order:create", "order:created"),       // fails
			Map("order:create", "audit:order-created"), // must NOT run - FailMessage stops
		},
		OnPublishError(func(src, evt benzene.Topic, err error) { hookSource, hookEvent, hookErr = src, evt, err }),
	)

	ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:create")}
	if err := mw(context.Background(), ic, setResult(ic, benzene.CreatedResult(orderDoc{ID: "o-1"}))); err != nil {
		t.Fatalf("middleware error = %v", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusUnexpectedError {
		t.Errorf("result status = %q, want 'unexpected-error' (FailMessage)", ic.Result.ResultStatus())
	}
	if len(pub.published) != 0 {
		t.Errorf("published %+v, want none - the second mapping must not run after a FailMessage stop", pub.published)
	}
	if hookSource.ID != "order:create" || hookEvent.ID != "order:created" || hookErr == nil {
		t.Errorf("OnPublishError got (%v, %v, %v), want (order:create, order:created, non-nil)", hookSource, hookEvent, hookErr)
	}
}

func TestMiddleware_LogAndContinueKeepsResultAndContinues(t *testing.T) {
	var hookCalls int
	pub := &capturingPublisher{failOn: map[string]error{"order:created": errors.New("broker down")}}
	mw := Middleware(pub,
		[]Mapping{
			Map("order:create", "order:created"),       // fails
			Map("order:create", "audit:order-created"), // still runs under LogAndContinue
		},
		OnPublishFailure(LogAndContinue),
		OnPublishError(func(benzene.Topic, benzene.Topic, error) { hookCalls++ }),
	)

	ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:create")}
	if err := mw(context.Background(), ic, setResult(ic, benzene.CreatedResult(orderDoc{ID: "o-1"}))); err != nil {
		t.Fatalf("middleware error = %v", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusCreated {
		t.Errorf("result status = %q, want unchanged 'created' (LogAndContinue keeps the response)", ic.Result.ResultStatus())
	}
	if len(pub.published) != 1 || pub.published[0].EventTopic.ID != "audit:order-created" {
		t.Errorf("published = %+v, want the second event to still go out after a LogAndContinue failure", pub.published)
	}
	if hookCalls != 1 {
		t.Errorf("OnPublishError called %d times, want 1", hookCalls)
	}
}

func TestMiddleware_FailureWithoutErrorHookDoesNotPanic(t *testing.T) {
	pub := &capturingPublisher{failOn: map[string]error{"order:created": errors.New("down")}}
	mw := Middleware(pub, []Mapping{Map("order:create", "order:created")}) // no OnPublishError
	ic := &benzene.InvocationContext{Topic: benzene.NewTopic("order:create")}
	if err := mw(context.Background(), ic, setResult(ic, benzene.CreatedResult(orderDoc{ID: "o-1"}))); err != nil {
		t.Fatalf("middleware error = %v", err)
	}
	if ic.Result.ResultStatus() != benzene.StatusUnexpectedError {
		t.Errorf("result status = %q, want 'unexpected-error'", ic.Result.ResultStatus())
	}
}

func TestNewSenderPublisher(t *testing.T) {
	t.Run("successful send publishes and returns nil", func(t *testing.T) {
		var gotTopic benzene.Topic
		var gotBody []byte
		sender := client.SenderFunc(func(_ context.Context, topic benzene.Topic, _ map[string]string, message []byte) benzene.Result[json.RawMessage] {
			gotTopic, gotBody = topic, message
			return benzene.Ok(json.RawMessage(`{}`))
		})
		err := NewSenderPublisher(sender).Publish(context.Background(), benzene.NewTopic("order:created"), orderDoc{ID: "o-1"})
		if err != nil {
			t.Fatalf("Publish() error = %v, want nil", err)
		}
		if gotTopic.ID != "order:created" || string(gotBody) != `{"id":"o-1"}` {
			t.Errorf("sent (%v, %s), want (order:created, {\"id\":\"o-1\"})", gotTopic, gotBody)
		}
	})

	t.Run("unsuccessful send with errors returns an error", func(t *testing.T) {
		sender := client.SenderFunc(func(context.Context, benzene.Topic, map[string]string, []byte) benzene.Result[json.RawMessage] {
			return benzene.ServiceUnavailable[json.RawMessage]("queue full")
		})
		err := NewSenderPublisher(sender).Publish(context.Background(), benzene.NewTopic("order:created"), orderDoc{ID: "o-1"})
		if err == nil {
			t.Fatal("Publish() error = nil, want an error for an unsuccessful send")
		}
	})

	t.Run("unsuccessful send without errors still returns an error", func(t *testing.T) {
		sender := client.SenderFunc(func(context.Context, benzene.Topic, map[string]string, []byte) benzene.Result[json.RawMessage] {
			return benzene.Fail[json.RawMessage](benzene.StatusConflict)
		})
		err := NewSenderPublisher(sender).Publish(context.Background(), benzene.NewTopic("order:created"), orderDoc{ID: "o-1"})
		if err == nil {
			t.Fatal("Publish() error = nil, want an error even when the failed result carries no error strings")
		}
	})

	t.Run("unmarshalable payload returns an error", func(t *testing.T) {
		sender := client.SenderFunc(func(context.Context, benzene.Topic, map[string]string, []byte) benzene.Result[json.RawMessage] {
			t.Fatal("sender must not be called when marshaling fails")
			return benzene.Ok(json.RawMessage(`{}`))
		})
		err := NewSenderPublisher(sender).Publish(context.Background(), benzene.NewTopic("order:created"), make(chan int))
		if err == nil {
			t.Fatal("Publish() error = nil, want a marshal error")
		}
	})
}

func TestMarshalPayload_VerbatimForBytes(t *testing.T) {
	raw := json.RawMessage(`{"already":"json"}`)
	if got, _ := marshalPayload(raw); string(got) != string(raw) {
		t.Errorf("marshalPayload(RawMessage) = %s, want verbatim %s", got, raw)
	}
	b := []byte("plain-bytes")
	if got, _ := marshalPayload(b); string(got) != "plain-bytes" {
		t.Errorf("marshalPayload([]byte) = %s, want verbatim", got)
	}
	if got, _ := marshalPayload(orderDoc{ID: "o-1"}); string(got) != `{"id":"o-1"}` {
		t.Errorf("marshalPayload(struct) = %s, want JSON", got)
	}
}

func TestPublisherFunc_Adapter(t *testing.T) {
	called := false
	var f Publisher = PublisherFunc(func(context.Context, benzene.Topic, any) error {
		called = true
		return nil
	})
	if err := f.Publish(context.Background(), benzene.NewTopic("x"), nil); err != nil || !called {
		t.Errorf("PublisherFunc did not delegate: called=%v err=%v", called, err)
	}
}
