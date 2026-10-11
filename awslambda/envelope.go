package awslambda

import (
	"context"
	"encoding/json"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// EnvelopeHandler adapts a HandlerFunc that receives the raw wire-contracts.md envelope directly
// and answers with the response envelope - transport-bindings.md's AWS Lambda catalog entry lists
// "the raw BenzeneMessage envelope for direct invocation" as one of the event shapes a Lambda
// binding selects between. Use it for a function its callers invoke synchronously (InvocationType
// RequestResponse, e.g. awslambdaclient's default), with no HTTP layer at all: the caller reads the
// outcome - failures included - from the envelope, so every processed envelope is a successful
// invocation (the envelope's request-response shape, conformance settlementDefaults "envelope").
//
// A function its callers invoke asynchronously (InvocationType Event) has no caller to read that
// envelope, so a failure returned in it would be silently dropped: use AsyncEnvelopeHandler there.
// The Lambda Runtime API does not tell a function which kind of invocation it received, which is
// why the two are separate entry points the application chooses between.
func EnvelopeHandler(builder *benzene.ApplicationBuilder) HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var req wire.Request
		if err := json.Unmarshal(event, &req); err != nil {
			return nil, fmt.Errorf("awslambda: malformed envelope: %w", err)
		}

		resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, req)
		return json.Marshal(resp)
	}
}

// Option configures AsyncEnvelopeHandler.
type Option func(*options)

type options struct {
	settlement envelope.Settlement
}

// WithSettlement sets how AsyncEnvelopeHandler settles an envelope whose invocation failed
// (transport-bindings.md §1, obligation 8). The default - the zero envelope.Settlement - fails the
// invocation so Lambda retries it; envelope.AtMostOnce completes the invocation whatever the
// outcome, and IgnoreUnmatchedTopics completes one whose topic no handler serves.
func WithSettlement(settlement envelope.Settlement) Option {
	return func(o *options) { o.settlement = settlement }
}

// AsyncEnvelopeHandler is the envelope entry point for a function its callers invoke asynchronously
// - a Lambda Event invocation, such as awslambdaclient with types.InvocationTypeEvent - where nobody
// reads the response envelope. It settles as a one-way binding (transport-bindings.md §1,
// obligation 8; conformance settlementDefaults "envelope-async", shape "one-shot"): an envelope the
// settlement does not acknowledge - by default a failure result, no result, or a pipeline error -
// fails the invocation with a Go error, so Lambda's asynchronous retry and then its on-failure
// destination or dead-letter queue take over instead of the failure being dropped. The unit of
// settlement is the invocation (one envelope), and delivery is at-least-once, so handlers must be
// idempotent. A successful invocation still returns the response envelope, which Lambda discards.
//
// Select it for a function deployed to be invoked asynchronously; a function invoked synchronously
// uses EnvelopeHandler, whose caller reads failures from the envelope itself. The Runtime API does
// not reveal the invocation type, so this is the application's choice, not a runtime detection.
func AsyncEnvelopeHandler(builder *benzene.ApplicationBuilder, opts ...Option) HandlerFunc {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var req wire.Request
		if err := json.Unmarshal(event, &req); err != nil {
			return nil, fmt.Errorf("awslambda: malformed envelope: %w", err)
		}

		outcome := o.settlement.Dispatch(ctx, builder.Pipeline, builder.Container, req)
		if !outcome.Acknowledge {
			return nil, fmt.Errorf("awslambda: envelope for topic %q was not successful (%s)", req.Topic, outcome.Response.StatusCode)
		}
		return json.Marshal(outcome.Response)
	}
}
