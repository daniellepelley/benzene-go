package responseevents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
)

// PublishFailureMode decides what the Middleware does when publishing a matched response event
// fails (the publisher returns an error).
type PublishFailureMode int

const (
	// FailMessage replaces the handler's response with an unexpected-error result, so the transport
	// reports the message as failed and (for a queue transport) redelivers it - honest at-least-once
	// delivery, so the handler and the event's consumers must be idempotent. It also stops publishing
	// any further matched events for this message. The default.
	FailMessage PublishFailureMode = iota
	// LogAndContinue invokes the OnPublishError hook and keeps the handler's response (the message is
	// acknowledged even though the event was lost), then continues with any remaining matches. For
	// pipelines where the follow-up event is best-effort.
	LogAndContinue
)

// Publisher is the outbound port the Middleware publishes matched events through. NewSenderPublisher
// adapts a client.Sender (the usual case); a test or an outbox relay implements it directly. Publish
// returns nil on success and an error when the event was not published (a transport error, or an
// unsuccessful result from the outbound route).
type Publisher interface {
	Publish(ctx context.Context, eventTopic benzene.Topic, payload any) error
}

// PublisherFunc adapts a plain function to a Publisher.
type PublisherFunc func(ctx context.Context, eventTopic benzene.Topic, payload any) error

// Publish calls f.
func (f PublisherFunc) Publish(ctx context.Context, eventTopic benzene.Topic, payload any) error {
	return f(ctx, eventTopic, payload)
}

// NewSenderPublisher builds a Publisher over an outbound client.Sender - the Go form of .NET's
// BenzeneMessageSenderResponseEventPublisher. The event topic needs an outbound route on the sender,
// and that route's own decorators (correlation id, trace context, retry) apply to the published
// event. The payload is JSON-marshalled (a []byte / json.RawMessage payload is sent verbatim); a
// send whose result is unsuccessful is reported as an error so the configured PublishFailureMode
// applies.
func NewSenderPublisher(sender client.Sender) Publisher {
	return PublisherFunc(func(ctx context.Context, eventTopic benzene.Topic, payload any) error {
		body, err := marshalPayload(payload)
		if err != nil {
			return fmt.Errorf("marshal event payload for %s: %w", eventTopic, err)
		}
		result := sender.Send(ctx, eventTopic, nil, body)
		if result.IsSuccessful() {
			return nil
		}
		if messages := result.ResultErrors(); len(messages) > 0 {
			return fmt.Errorf("publish to %s returned status %q: %s", eventTopic, result.Status, strings.Join(messages, "; "))
		}
		return fmt.Errorf("publish to %s returned status %q", eventTopic, result.Status)
	})
}

// marshalPayload renders an event payload into the bytes the Sender carries: a []byte /
// json.RawMessage is used verbatim (already-serialized), anything else is JSON-marshalled.
func marshalPayload(payload any) ([]byte, error) {
	switch v := payload.(type) {
	case json.RawMessage:
		return v, nil
	case []byte:
		return v, nil
	default:
		return json.Marshal(payload)
	}
}

// Option configures the Middleware.
type Option func(*config)

type config struct {
	failureMode PublishFailureMode
	onError     func(sourceTopic, eventTopic benzene.Topic, err error)
}

// OnPublishFailure sets the failure mode (default FailMessage).
func OnPublishFailure(mode PublishFailureMode) Option {
	return func(c *config) { c.failureMode = mode }
}

// OnPublishError registers a hook called whenever publishing a matched event fails, under either
// failure mode (the Go stand-in for the .NET middleware's optional ILogger). Use it to log the
// failure; the package itself never logs, staying dependency-free.
func OnPublishError(hook func(sourceTopic, eventTopic benzene.Topic, err error)) Option {
	return func(c *config) { c.onError = hook }
}

// Middleware returns a pipeline middleware that republishes a handler's response as follow-up events
// per the given mappings: after the handler runs, every mapping that matches the (source topic,
// result) pair publishes its event through publisher. Register it before the router (so its
// post-handler block sees the populated result). Fan-out is allowed - all matching mappings publish.
//
// On a publish failure: FailMessage (default) replaces the result with an unexpected-error and stops
// (the message nacks/redelivers); LogAndContinue keeps the result and continues with the remaining
// matches. With no mappings, or when none match, the middleware is a pass-through.
func Middleware(publisher Publisher, mappings []Mapping, opts ...Option) benzene.Middleware {
	cfg := config{failureMode: FailMessage}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		if err := next(ctx); err != nil {
			return err
		}
		if ic.Result == nil {
			return nil
		}

		// Mappings are resolved-then-published in one pass (rather than resolving all first, as .NET
		// does). The set of published events is identical; the only observable difference is that after
		// a FailMessage stop, a later mapping's When/Project is not evaluated - so those closures must
		// be side-effect-free (they are pure predicates/projections by contract).
		for _, mapping := range mappings {
			publication := mapping.Resolve(ic.Topic, ic.Result)
			if publication == nil {
				continue
			}
			if err := publisher.Publish(ctx, publication.EventTopic, publication.Payload); err != nil {
				if cfg.onError != nil {
					cfg.onError(ic.Topic, publication.EventTopic, err)
				}
				if cfg.failureMode == FailMessage {
					ic.Result = benzene.UnexpectedError[any](
						fmt.Sprintf("failed to publish response event %q: %v", publication.EventTopic.String(), err))
					return nil
				}
			}
		}
		return nil
	}
}

// resultSuccessful reads the result's success flag off the type-erased ResultInfo (via the optional
// ResultIsSuccessful interface every Result[T] satisfies), falling back to the status class - the
// same rule the envelope dispatch and idempotency middleware use.
func resultSuccessful(result benzene.ResultInfo) bool {
	if result == nil {
		return false
	}
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		return s.ResultIsSuccessful()
	}
	return !result.ResultStatus().IsFailure()
}
