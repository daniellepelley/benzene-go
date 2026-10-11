package envelope

import (
	"context"
	"errors"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// settlementApp serves "greet" (ok), "fail" (a handler's own not-found) and "ignore" (ignored); any
// other topic is unmatched.
func settlementApp(t *testing.T) (*benzene.Container, *benzene.Pipeline) {
	t.Helper()
	registry := benzene.NewRegistry()
	benzene.MustRegister(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler))
	benzene.MustRegister(registry, benzene.NewTopic("fail"), func(context.Context, greetRequest) benzene.Result[greetResponse] {
		return benzene.NotFound[greetResponse]("no such greeting")
	})
	benzene.MustRegister(registry, benzene.NewTopic("ignore"), func(context.Context, greetRequest) benzene.Result[greetResponse] {
		return benzene.Ignored(greetResponse{})
	})
	return benzene.NewContainer(), benzene.NewPipeline(benzene.RouterMiddleware(registry))
}

func TestSettlement_AcknowledgesPerTheSpec(t *testing.T) {
	container, pipeline := settlementApp(t)
	tests := []struct {
		name           string
		settlement     Settlement
		topic          string
		wantSuccessful bool
		wantAck        bool
	}{
		// The zero value: transport-bindings.md §1 obligation 8's default.
		{"success is acknowledged", Settlement{}, "greet", true, true},
		{"ignored is acknowledged", Settlement{}, "ignore", true, true},
		{"a failure result is redelivered", Settlement{}, "fail", false, false},
		{"an unmatched topic is redelivered", Settlement{}, "someone:elses", false, false},
		{"explicit at-least-once is the default", Settlement{Delivery: AtLeastOnce}, "fail", false, false},
		// The override every one-way binding offers.
		{"at-most-once acknowledges a failure", Settlement{Delivery: AtMostOnce}, "fail", false, true},
		{"at-most-once acknowledges an unmatched topic", Settlement{Delivery: AtMostOnce}, "someone:elses", false, true},
		// Unmatched-as-ignored is about unmatched topics only.
		{"ignoring unmatched topics acknowledges one", Settlement{IgnoreUnmatchedTopics: true}, "someone:elses", false, true},
		{"ignoring unmatched topics still redelivers a handler's not-found", Settlement{IgnoreUnmatchedTopics: true}, "fail", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.settlement.Dispatch(context.Background(), pipeline, container, wire.Request{Topic: tt.topic, Body: `{"name":"x"}`})
			if got.Successful != tt.wantSuccessful || got.Acknowledge != tt.wantAck {
				t.Errorf("Outcome = {Successful: %v, Acknowledge: %v}, want {%v, %v}", got.Successful, got.Acknowledge, tt.wantSuccessful, tt.wantAck)
			}
			if got.Response.StatusCode == "" {
				t.Error("Outcome.Response is empty")
			}
		})
	}
}

func TestSettlement_NoResultAndPipelineErrorsAreFailures(t *testing.T) {
	container := benzene.NewContainer()
	silent := benzene.NewPipeline(func(context.Context, *benzene.InvocationContext, func(context.Context) error) error { return nil })
	failing := benzene.NewPipeline(func(context.Context, *benzene.InvocationContext, func(context.Context) error) error {
		return errors.New("broker down")
	})

	for name, pipeline := range map[string]*benzene.Pipeline{"no result": silent, "a pipeline error": failing} {
		t.Run(name, func(t *testing.T) {
			if got := (Settlement{}).DispatchTopic(context.Background(), pipeline, container, benzene.NewTopic("t"), nil, ""); got.Successful || got.Acknowledge {
				t.Errorf("default settlement = %+v, want a redelivered failure", got)
			}
			// A middleware that ends the pipeline without a result is not an unmatched topic.
			if got := (Settlement{IgnoreUnmatchedTopics: true}).DispatchTopic(context.Background(), pipeline, container, benzene.NewTopic("t"), nil, ""); got.Acknowledge {
				t.Errorf("IgnoreUnmatchedTopics acknowledged %s", name)
			}
			if got := (Settlement{Delivery: AtMostOnce}).DispatchTopic(context.Background(), pipeline, container, benzene.NewTopic("t"), nil, ""); !got.Acknowledge {
				t.Errorf("AtMostOnce did not acknowledge %s", name)
			}
		})
	}
}

func TestDelivery_String(t *testing.T) {
	for d, want := range map[Delivery]string{BindingDefault: "binding-default", AtLeastOnce: "at-least-once", AtMostOnce: "at-most-once"} {
		if got := d.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", d, got, want)
		}
	}
}
