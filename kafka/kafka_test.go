package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
	kafkago "github.com/segmentio/kafka-go"
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

// fakeSource feeds a fixed slice of messages; once drained it cancels the run's context and
// reports the cancellation, the shape a real reader takes when its context is cancelled with
// nothing left to fetch. commitErr, when non-nil, fails every commit. Everything runs on
// Run's own goroutine, so no synchronization is needed.
type fakeSource struct {
	messages  []kafkago.Message
	fetchErr  error
	commitErr error
	committed []kafkago.Message
	cancel    context.CancelFunc
	// cancelOnCommit cancels the run's context before a failing commit returns - a commit
	// interrupted by shutdown, which Run must treat as clean shutdown, not a source failure.
	cancelOnCommit bool
}

func (s *fakeSource) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	if s.fetchErr != nil {
		return kafkago.Message{}, s.fetchErr
	}
	if len(s.messages) == 0 {
		s.cancel()
		return kafkago.Message{}, context.Canceled
	}
	msg := s.messages[0]
	s.messages = s.messages[1:]
	return msg, nil
}

func (s *fakeSource) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	if s.commitErr != nil {
		if s.cancelOnCommit {
			s.cancel()
		}
		return s.commitErr
	}
	s.committed = append(s.committed, msgs...)
	return nil
}

// runUntilDrained runs the consumer until the source has fed every message, at which point
// the source cancels the context and Run's clean-shutdown path returns nil.
func runUntilDrained(t *testing.T, consumer *Consumer, source *fakeSource) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source.cancel = cancel
	return consumer.Run(ctx)
}

func topicMessage(name string) kafkago.Message {
	return kafkago.Message{
		Headers: []kafkago.Header{{Key: "topic", Value: []byte("greet")}},
		Value:   []byte(`{"name":"` + name + `"}`),
	}
}

func TestConsumer_DispatchesAndCommits(t *testing.T) {
	source := &fakeSource{messages: []kafkago.Message{topicMessage("One"), topicMessage("Two")}}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t)}

	if err := runUntilDrained(t, consumer, source); err != nil {
		t.Fatalf("Run() error = %v, want nil on cancellation", err)
	}
	if len(source.committed) != 2 {
		t.Errorf("committed %d messages, want 2", len(source.committed))
	}
}

func TestConsumer_FailureInvokesOnFailureThenCommits(t *testing.T) {
	tests := []struct {
		name       string
		message    kafkago.Message
		wantStatus benzene.Status
	}{
		{name: "handler failure status", message: topicMessage(""), wantStatus: benzene.StatusBadRequest},
		{
			name:       "no topic resolvable",
			message:    kafkago.Message{Value: []byte("just some text")},
			wantStatus: benzene.StatusValidationError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSource{messages: []kafkago.Message{tt.message}}
			var failed []wire.Response
			consumer := &Consumer{
				Source:  source,
				Builder: newTestBuilder(t),
				OnFailure: func(_ context.Context, _ kafkago.Message, resp wire.Response) {
					failed = append(failed, resp)
				},
			}

			if err := runUntilDrained(t, consumer, source); err != nil {
				t.Fatalf("Run() error = %v, want nil", err)
			}
			if len(failed) != 1 {
				t.Fatalf("OnFailure called %d times, want 1", len(failed))
			}
			if failed[0].StatusCode != string(tt.wantStatus) {
				t.Errorf("failed StatusCode = %q, want %q", failed[0].StatusCode, tt.wantStatus)
			}
			if len(source.committed) != 1 {
				t.Errorf("committed %d messages, want 1 (failures commit too - see package doc)", len(source.committed))
			}
		})
	}
}

func TestConsumer_FetchErrorReturns(t *testing.T) {
	source := &fakeSource{fetchErr: errors.New("broker gone")}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t)}

	err := consumer.Run(context.Background())
	if err == nil || !errors.Is(err, source.fetchErr) {
		t.Errorf("Run() error = %v, want the wrapped fetch error", err)
	}
}

func TestConsumer_CommitErrorReturns(t *testing.T) {
	source := &fakeSource{messages: []kafkago.Message{topicMessage("One")}, commitErr: errors.New("commit refused")}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t)}

	err := consumer.Run(context.Background())
	if err == nil || !errors.Is(err, source.commitErr) {
		t.Errorf("Run() error = %v, want the wrapped commit error", err)
	}
}

func TestConsumer_CommitErrorDuringShutdownIsClean(t *testing.T) {
	source := &fakeSource{
		messages:       []kafkago.Message{topicMessage("One")},
		commitErr:      errors.New("interrupted"),
		cancelOnCommit: true,
	}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t)}

	if err := runUntilDrained(t, consumer, source); err != nil {
		t.Errorf("Run() error = %v, want nil when the commit failure is a cancellation", err)
	}
}

func TestConsumer_CancelledContextIsCleanShutdown(t *testing.T) {
	source := &fakeSource{}
	consumer := &Consumer{Source: source, Builder: newTestBuilder(t)}

	if err := runUntilDrained(t, consumer, source); err != nil {
		t.Errorf("Run() error = %v, want nil for a cancelled context", err)
	}
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

func TestResolveRequest(t *testing.T) {
	envelopeBody, err := json.Marshal(wire.Request{Topic: "greet", Headers: map[string]string{"from-envelope": "e"}, Body: `{}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	tests := []struct {
		name        string
		message     kafkago.Message
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name: "topic header wins, case-insensitively, others become headers",
			message: kafkago.Message{
				Headers: []kafkago.Header{
					{Key: "Topic", Value: []byte("greet")},
					{Key: "x-correlation-id", Value: []byte("abc")},
				},
				Value: []byte(`{"name":"World"}`),
			},
			wantTopic:   "greet",
			wantBody:    `{"name":"World"}`,
			wantHeaders: map[string]string{"x-correlation-id": "abc"},
		},
		{
			name: "duplicate header keys - last value wins",
			message: kafkago.Message{
				Headers: []kafkago.Header{
					{Key: "topic", Value: []byte("greet")},
					{Key: "x-tag", Value: []byte("first")},
					{Key: "x-tag", Value: []byte("last")},
				},
			},
			wantTopic:   "greet",
			wantBody:    "",
			wantHeaders: map[string]string{"x-tag": "last"},
		},
		{
			name:        "envelope in value merges headers",
			message:     kafkago.Message{Value: envelopeBody},
			wantTopic:   "greet",
			wantBody:    `{}`,
			wantHeaders: map[string]string{"from-envelope": "e"},
		},
		{
			name:        "nothing resolvable yields empty topic and raw value",
			message:     kafkago.Message{Value: []byte("plain text")},
			wantTopic:   "",
			wantBody:    "plain text",
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := resolveRequest(tt.message)
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
