package rabbitmq

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

// fakeAcknowledger records the settlement calls a delivery delegates to it, so tests can assert
// ack vs nack (and the requeue flag) without a live channel. It satisfies amqp.Acknowledger, which
// is what amqp.Delivery.Ack/Nack delegate to.
type fakeAcknowledger struct {
	acks  []uint64
	nacks []nackCall
}

type nackCall struct {
	tag     uint64
	requeue bool
}

func (a *fakeAcknowledger) Ack(tag uint64, _ bool) error {
	a.acks = append(a.acks, tag)
	return nil
}

func (a *fakeAcknowledger) Nack(tag uint64, _, requeue bool) error {
	a.nacks = append(a.nacks, nackCall{tag, requeue})
	return nil
}

func (a *fakeAcknowledger) Reject(_ uint64, _ bool) error { return nil }

// fakeSource feeds a fixed set of deliveries over a channel it closes once drained; Run reports the
// close as errDeliveriesClosed (a broker drop while the context is live). A consumeErr, when set,
// fails the initial Consume call. All deliveries share one fakeAcknowledger so a test reads the
// settlements off it.
type fakeSource struct {
	deliveries []amqp.Delivery
	consumeErr error
}

func (s *fakeSource) Consume(_, _ string, _, _, _, _ bool, _ amqp.Table) (<-chan amqp.Delivery, error) {
	if s.consumeErr != nil {
		return nil, s.consumeErr
	}
	ch := make(chan amqp.Delivery, len(s.deliveries))
	for _, d := range s.deliveries {
		ch <- d
	}
	close(ch)
	return ch, nil
}

func delivery(ack *fakeAcknowledger, tag uint64, headers amqp.Table, routingKey, body string) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger: ack,
		DeliveryTag:  tag,
		Headers:      headers,
		RoutingKey:   routingKey,
		Body:         []byte(body),
	}
}

func TestConsumer_DispatchesAndAcks(t *testing.T) {
	ack := &fakeAcknowledger{}
	source := &fakeSource{deliveries: []amqp.Delivery{
		delivery(ack, 1, amqp.Table{"topic": []byte("greet")}, "", `{"name":"One"}`),
		delivery(ack, 2, amqp.Table{"topic": []byte("greet")}, "", `{"name":"Two"}`),
	}}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t), Queue: "q"}

	// The fake closes the stream after its buffered deliveries, which Run reports as a broker close.
	if err := consumer.Run(context.Background()); !errors.Is(err, errDeliveriesClosed) {
		t.Fatalf("Run() error = %v, want errDeliveriesClosed after the stream drains", err)
	}
	if len(ack.acks) != 2 {
		t.Errorf("acked %v, want both deliveries acked", ack.acks)
	}
	if len(ack.nacks) != 0 {
		t.Errorf("nacked %v, want none on success", ack.nacks)
	}
}

func TestConsumer_FailureNacksWithRequeueAndInvokesOnFailure(t *testing.T) {
	tests := []struct {
		name       string
		delivery   func(*fakeAcknowledger) amqp.Delivery
		noRequeue  bool
		wantStatus benzene.Status
		wantNack   bool // whether the nack should requeue
	}{
		{
			name: "first-attempt handler failure requeues",
			delivery: func(a *fakeAcknowledger) amqp.Delivery {
				return delivery(a, 1, amqp.Table{"topic": []byte("greet")}, "", `{"name":""}`)
			},
			wantStatus: benzene.StatusBadRequest,
			wantNack:   true,
		},
		{
			name: "already-redelivered failure is not requeued (poison bound)",
			delivery: func(a *fakeAcknowledger) amqp.Delivery {
				d := delivery(a, 1, amqp.Table{"topic": []byte("greet")}, "", `{"name":""}`)
				d.Redelivered = true
				return d
			},
			wantStatus: benzene.StatusBadRequest,
			wantNack:   false,
		},
		{
			name: "NoRequeue routes straight to the DLX",
			delivery: func(a *fakeAcknowledger) amqp.Delivery {
				return delivery(a, 1, amqp.Table{"topic": []byte("greet")}, "", `{"name":""}`)
			},
			noRequeue:  true,
			wantStatus: benzene.StatusBadRequest,
			wantNack:   false,
		},
		{
			name:       "unroutable delivery yields no route",
			delivery:   func(a *fakeAcknowledger) amqp.Delivery { return delivery(a, 1, nil, "", "just some text") },
			wantStatus: benzene.StatusValidationError,
			wantNack:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ack := &fakeAcknowledger{}
			source := &fakeSource{deliveries: []amqp.Delivery{tt.delivery(ack)}}
			var failed []wire.Response
			consumer := &Consumer{
				Source:    source,
				Builder:   newTestBuilder(t),
				Queue:     "q",
				NoRequeue: tt.noRequeue,
				OnFailure: func(_ context.Context, _ amqp.Delivery, resp wire.Response) {
					failed = append(failed, resp)
				},
			}

			if err := consumer.Run(context.Background()); !errors.Is(err, errDeliveriesClosed) {
				t.Fatalf("Run() error = %v, want errDeliveriesClosed", err)
			}
			if len(failed) != 1 {
				t.Fatalf("OnFailure called %d times, want 1", len(failed))
			}
			if failed[0].StatusCode != string(tt.wantStatus) {
				t.Errorf("failed StatusCode = %q, want %q", failed[0].StatusCode, tt.wantStatus)
			}
			if len(ack.acks) != 0 {
				t.Errorf("acked %v, want none on failure", ack.acks)
			}
			if len(ack.nacks) != 1 {
				t.Fatalf("nacked %d times, want 1", len(ack.nacks))
			}
			if ack.nacks[0].requeue != tt.wantNack {
				t.Errorf("nack requeue = %v, want %v", ack.nacks[0].requeue, tt.wantNack)
			}
		})
	}
}

func TestConsumer_ConsumeErrorReturns(t *testing.T) {
	source := &fakeSource{consumeErr: errors.New("no such queue")}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t), Queue: "q"}

	err := consumer.Run(context.Background())
	if err == nil || !errors.Is(err, source.consumeErr) {
		t.Errorf("Run() error = %v, want the wrapped consume error", err)
	}
}

func TestConsumer_CancelledContextIsCleanShutdown(t *testing.T) {
	// An open, never-closed stream + a cancelled context: Run takes the ctx.Done path and returns nil.
	source := &openSource{ch: make(chan amqp.Delivery)}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t), Queue: "q"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := consumer.Run(ctx); err != nil {
		t.Errorf("Run() error = %v, want nil for a cancelled context", err)
	}
}

// erredContext reports a cancellation via Err() but never fires Done(), so a select deterministically
// takes the ready (closed) delivery case rather than racing the Done() case - letting the test drive
// Run's "stream closed while the context is already cancelled" branch without flakiness.
type erredContext struct{ context.Context }

func (erredContext) Done() <-chan struct{} { return nil }
func (erredContext) Err() error            { return context.Canceled }

func TestConsumer_StreamClosedDuringShutdownIsClean(t *testing.T) {
	// The stream closes AND the context is already cancelled: the close is a graceful shutdown, nil.
	ch := make(chan amqp.Delivery)
	close(ch)
	source := &openSource{ch: ch}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t), Queue: "q"}

	if err := consumer.Run(erredContext{context.Background()}); err != nil {
		t.Errorf("Run() error = %v, want nil when the stream closes under a cancelled context", err)
	}
}

// openSource returns a caller-supplied channel verbatim (not drained/closed like fakeSource), for
// the shutdown-path tests that drive Run via the context rather than the stream.
type openSource struct{ ch chan amqp.Delivery }

func (s *openSource) Consume(_, _ string, _, _, _, _ bool, _ amqp.Table) (<-chan amqp.Delivery, error) {
	return s.ch, nil
}

func TestConsumer_Validate(t *testing.T) {
	builder := newTestBuilder(t)
	tests := []struct {
		name     string
		consumer *Consumer
		wantErr  bool
	}{
		{name: "runnable", consumer: &Consumer{Source: &fakeSource{}, Builder: builder}, wantErr: false},
		{name: "missing source", consumer: &Consumer{Builder: builder}, wantErr: true},
		{name: "missing builder", consumer: &Consumer{Source: &fakeSource{}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.consumer.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestConsumer_ResolveRequest(t *testing.T) {
	builder := newTestBuilder(t)
	tests := []struct {
		name          string
		reservedNames wire.ReservedNames
		delivery      amqp.Delivery
		wantTopic     string
		wantBody      string
		wantHeaders   map[string]string
	}{
		{
			name: "topic header resolves, other headers pass through, topic header is stripped",
			delivery: amqp.Delivery{
				Headers: amqp.Table{
					"topic":            []byte("greet"),
					"x-correlation-id": []byte("abc"),
					"traceparent":      "00-1-2-01", // raw string value accepted too
				},
				RoutingKey: "ignored-when-header-present",
				Body:       []byte(`{"name":"World"}`),
			},
			wantTopic:   "greet",
			wantBody:    `{"name":"World"}`,
			wantHeaders: map[string]string{"x-correlation-id": "abc", "traceparent": "00-1-2-01"},
		},
		{
			name: "routing-key fallback when no topic header",
			delivery: amqp.Delivery{
				Headers:    amqp.Table{"x-tag": []byte("v")},
				RoutingKey: "greet",
				Body:       []byte(`{"name":"World"}`),
			},
			wantTopic:   "greet",
			wantBody:    `{"name":"World"}`,
			wantHeaders: map[string]string{"x-tag": "v"},
		},
		{
			name: "envelope-in-body fallback when no header and no routing key",
			delivery: amqp.Delivery{
				Body: []byte(`{"topic":"greet","headers":{"x-env":"1"},"body":"{\"name\":\"World\"}"}`),
			},
			wantTopic:   "greet",
			wantBody:    `{"name":"World"}`,
			wantHeaders: map[string]string{"x-env": "1"},
		},
		{
			name: "unroutable delivery yields an empty topic",
			delivery: amqp.Delivery{
				Body: []byte("plain text"),
			},
			wantTopic:   "",
			wantBody:    "plain text",
			wantHeaders: map[string]string{},
		},
		{
			name:          "overridden topic header key",
			reservedNames: wire.ReservedNames{TopicKey: "benzene-topic"},
			delivery: amqp.Delivery{
				Headers: amqp.Table{"benzene-topic": []byte("greet")},
				Body:    []byte(`{"name":"World"}`),
			},
			wantTopic:   "greet",
			wantBody:    `{"name":"World"}`,
			wantHeaders: map[string]string{},
		},
		{
			// A nil-valued topic header decodes to "" and is stripped as the (empty) topic, so it
			// yields no route on its own and the routing key resolves it instead.
			name: "nil topic header value falls back to the routing key",
			delivery: amqp.Delivery{
				Headers:    amqp.Table{"topic": nil},
				RoutingKey: "greet",
				Body:       []byte(`{"name":"World"}`),
			},
			wantTopic:   "greet",
			wantBody:    `{"name":"World"}`,
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumer := &Consumer{Source: &fakeSource{}, Builder: builder, ReservedNames: tt.reservedNames}
			req := consumer.resolveRequest(tt.delivery)
			if req.Topic != tt.wantTopic {
				t.Errorf("Topic = %q, want %q", req.Topic, tt.wantTopic)
			}
			if req.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q", req.Body, tt.wantBody)
			}
			if len(req.Headers) != len(tt.wantHeaders) {
				t.Fatalf("Headers = %v, want %v", req.Headers, tt.wantHeaders)
			}
			for k, v := range tt.wantHeaders {
				if req.Headers[k] != v {
					t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
				}
			}
		})
	}
}

func TestHeaderString(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "bytes are UTF-8 decoded", value: []byte("hello"), want: "hello"},
		{name: "raw string passes through", value: "hello", want: "hello"},
		{name: "nil is empty", value: nil, want: ""},
		{name: "other types are stringified", value: 42, want: "42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := headerString(tt.value); got != tt.want {
				t.Errorf("headerString(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
