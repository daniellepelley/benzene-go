package envelope

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// Delivery is the guarantee a one-way binding gives a message whose invocation failed - its result
// was unsuccessful, there was no result, or the pipeline returned an error (transport-bindings.md
// §1, obligation 8).
type Delivery int

const (
	// BindingDefault, the zero value, is the binding's documented default: AtLeastOnce on every
	// binding in this port except a stream that documents skip-and-continue (kafka.Consumer).
	BindingDefault Delivery = iota
	// AtLeastOnce never acknowledges a failed message: the transport redelivers it (a batch-item
	// failure, a Go error, a non-2xx, a nack, an abandon, or a withheld delete, commit or
	// checkpoint) until it succeeds or the transport's own redrive configuration dead-letters it.
	// Handlers must therefore be idempotent.
	AtLeastOnce
	// AtMostOnce acknowledges every message whatever its outcome - the override every one-way
	// binding offers. A failed message is not redelivered; on a stream this is skip-and-continue.
	// The binding's failure hook, where it has one, still sees the failure.
	AtMostOnce
)

// String returns the delivery's name, for logs and test failures.
func (d Delivery) String() string {
	switch d {
	case AtLeastOnce:
		return "at-least-once"
	case AtMostOnce:
		return "at-most-once"
	default:
		return "binding-default"
	}
}

// Settlement is how a one-way binding - a queue, topic, stream or event trigger, with no reply
// channel - decides whether the transport may forget each message (transport-bindings.md §1,
// obligation 8). Every one-way binding in this port takes one: the handler constructors through a
// WithSettlement option, the self-hosted workers as a Settlement field.
//
// The zero value is the spec's default: acknowledge a message only when its invocation succeeded
// (isSuccessful: true, ignored included), and redeliver a failure result, a missing result, an
// error and an unmatched topic alike - so handlers on one-way bindings must be idempotent.
// Dead-lettering is the transport's own redrive configuration; Benzene only declines the
// acknowledgement.
type Settlement struct {
	// Delivery is AtLeastOnce or AtMostOnce; the zero value is the binding's default.
	Delivery Delivery
	// IgnoreUnmatchedTopics acknowledges a message whose topic no handler serves at any version
	// (benzene.IsUnmatchedTopic) as though it had been ignored, instead of redelivering it - for a
	// subscriber on a shared bus that sees other services' topics. A handler's own not-found and a
	// version the topic does not serve are still failures.
	IgnoreUnmatchedTopics bool
}

// Outcome is how one message's invocation ended, as a one-way binding settles it.
type Outcome struct {
	// Response is the invocation's response envelope, for a failure hook to log or a binding with a
	// reply to send.
	Response wire.Response
	// Successful is the invocation's isSuccessful: false for a failure result, for no result, and
	// for an error the pipeline returned.
	Successful bool
	// Acknowledge reports whether the binding should settle the message as done under the
	// Settlement - true when it succeeded, and also for a failure the Settlement accepts
	// (AtMostOnce, or an unmatched topic with IgnoreUnmatchedTopics).
	Acknowledge bool
}

// Dispatch is DispatchResult under s: it dispatches req and reports whether to acknowledge it.
func (s Settlement) Dispatch(ctx context.Context, pipeline *benzene.Pipeline, container *benzene.Container, req wire.Request) Outcome {
	return s.DispatchTopic(ctx, pipeline, container, benzene.NewTopic(req.Topic), req.Headers, req.Body)
}

// DispatchTopic is DispatchTopicResult under s: it dispatches an explicit topic (a fan-in binding's
// code-named one, say) and reports whether to acknowledge the message.
func (s Settlement) DispatchTopic(ctx context.Context, pipeline *benzene.Pipeline, container *benzene.Container, topic benzene.Topic, headers map[string]string, body string) Outcome {
	resp, successful, unmatched := dispatch(ctx, pipeline, container, topic, headers, body)
	return Outcome{
		Response:    resp,
		Successful:  successful,
		Acknowledge: successful || s.Delivery == AtMostOnce || (s.IgnoreUnmatchedTopics && unmatched),
	}
}
