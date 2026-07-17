// Package awseventbridge is the AWS EventBridge binding: a Lambda function invoked by an
// EventBridge rule (inbound, zero new dependency - AWS delivers the event as plain JSON to
// the invocation, the same shape as awslambda's and awssns's hand-rolled adapters) plus an
// outbound publish client (needs aws-sdk-go-v2/service/eventbridge - see client.go), which
// is why this package lives in its own Go module (see RELEASING.md).
//
// EventBridge events have no per-message attribute map, so the SQS/SNS "topic attribute"
// convention has nowhere to live. Instead the mapping leans on the two channels an event
// does have (mirroring how the cloudevents package maps `type`):
//
//   - detail-type carries the Benzene topic - it is EventBridge's own semantic "what kind of
//     event" field and what rules pattern-match on, so a rule can route per Benzene topic.
//   - detail carries the payload. Client always writes it as a full wire envelope
//     (topic/headers/body) so wire headers survive the trip; Handler unwraps an
//     envelope-shaped detail and otherwise treats detail verbatim as the body with the topic
//     from detail-type - so events from non-Benzene producers (a rule matching AWS service
//     events, a partner event source) dispatch too.
//
// Like a direct SNS subscription, a rule-invoked Lambda is an asynchronous invocation with
// no batch or partial-failure concept: a failed event is reported by returning a Go error -
// triggering AWS's own async-invoke retry and, if configured, its dead-letter queue.
package awseventbridge

import (
	"context"
	"encoding/json"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// ruleEvent mirrors the fields this adapter needs from the event an EventBridge rule
// delivers to Lambda - see
// https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-events-structure.html.
type ruleEvent struct {
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	Detail     json.RawMessage `json:"detail"`
}

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda invoked by an
// EventBridge rule. The event runs through the pipeline with its own DI scope, via
// envelope.Dispatch. A non-success dispatch result returns a Go error - triggering AWS's own
// async-invoke retry - since EventBridge-to-Lambda has no partial-failure response shape to
// report to, the same posture as awssns.
func Handler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var rule ruleEvent
		if err := json.Unmarshal(event, &rule); err != nil {
			return nil, fmt.Errorf("awseventbridge: malformed EventBridge event: %w", err)
		}

		resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, resolveRequest(rule))
		if !benzene.Status(resp.StatusCode).IsSuccess() {
			return nil, fmt.Errorf("awseventbridge: event %s failed: %s", rule.ID, resp.StatusCode)
		}
		return json.Marshal(struct{}{})
	}
}

// resolveRequest resolves the event per the package doc's mapping: an envelope-shaped detail
// is unwrapped (its topic and headers win - it is the only channel wire headers can travel
// on, and Client always writes it); otherwise the topic is detail-type with detail verbatim
// as the body. The event's own id and source are always available as the "id" and "source"
// headers (envelope headers of the same name win). An event yielding no topic at all carries
// an empty topic, which RouterMiddleware maps to ValidationError - surfaced as a Go error
// (see Handler), never a silently dropped event.
func resolveRequest(rule ruleEvent) wire.Request {
	headers := map[string]string{}
	if rule.ID != "" {
		headers["id"] = rule.ID
	}
	if rule.Source != "" {
		headers["source"] = rule.Source
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal(rule.Detail, &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}

	return wire.Request{Topic: rule.DetailType, Headers: headers, Body: string(rule.Detail)}
}
