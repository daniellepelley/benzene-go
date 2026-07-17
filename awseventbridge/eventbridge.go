// Package awseventbridge is the AWS EventBridge binding
// (docs/specification/transport-bindings.md's EventBridge entry in the main repo): a Lambda
// function invoked by an EventBridge rule (inbound, zero new dependency - AWS delivers the
// event as plain JSON to the invocation, the same shape as awslambda's and awssns's
// hand-rolled adapters) plus an outbound publish client (needs
// aws-sdk-go-v2/service/eventbridge - see client.go), which is why this package lives in its
// own Go module (see RELEASING.md).
//
// The spec's mapping, applied here exactly:
//
//   - Topic: the event's detail-type, verbatim - EventBridge's own native routing key, so
//     this binding needs no bolted-on "topic" attribute. source is metadata, not part of the
//     topic.
//   - Body: the raw JSON of detail (the domain payload) - EmbeddedHeadersKey, when present,
//     is an extra field a request mapper's deserialization simply ignores, exactly as the
//     spec notes.
//   - Headers: envelope metadata under "eventbridge-"-prefixed keys (id/source/account/
//     region/time/detail-type), plus Benzene wire headers lifted from the reserved
//     EmbeddedHeadersKey object inside detail (wire-contracts.md §2) - EventBridge has no
//     native per-message attributes, so the outbound Client embeds them there. Embedded
//     headers win over the eventbridge- prefixed ones on key collision.
//
// One event per invocation (EventBridge does not batch Lambda targets) - one pipeline
// invocation, one DI scope; fire-and-forget (no response channel). Like a direct SNS
// subscription, a rule-invoked Lambda is an asynchronous invocation with no partial-failure
// concept: a failed event is reported by returning a Go error - triggering AWS's own
// async-invoke retry and, if configured, its dead-letter queue.
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

// EmbeddedHeadersKey is the reserved key inside detail that carries embedded Benzene wire
// headers - must match the main repo's
// Benzene.Aws.Lambda.EventBridge.EventBridgeMessageHeadersGetter.EmbeddedHeadersKey /
// Benzene.Clients.Aws.EventBridge.EventBridgeContextConverter.EmbeddedHeadersKey.
const EmbeddedHeadersKey = "_benzeneHeaders"

// ruleEvent mirrors the fields this adapter needs from the event an EventBridge rule
// delivers to Lambda - see
// https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-events-structure.html.
type ruleEvent struct {
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	Account    string          `json:"account"`
	Region     string          `json:"region"`
	Time       string          `json:"time"`
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

// resolveRequest resolves the event per the package doc's mapping: topic is detail-type
// verbatim, body is the raw detail JSON, and headers are the eventbridge-prefixed envelope
// metadata with any _benzeneHeaders object inside detail lifted on top (embedded wins on key
// collision, matching the main repo's EventBridgeMessageHeadersGetter).
func resolveRequest(rule ruleEvent) wire.Request {
	headers := map[string]string{}
	addIfPresent(headers, "eventbridge-id", rule.ID)
	addIfPresent(headers, "eventbridge-source", rule.Source)
	addIfPresent(headers, "eventbridge-account", rule.Account)
	addIfPresent(headers, "eventbridge-region", rule.Region)
	addIfPresent(headers, "eventbridge-time", rule.Time)
	addIfPresent(headers, "eventbridge-detail-type", rule.DetailType)

	for k, v := range embeddedHeaders(rule.Detail) {
		headers[k] = v
	}

	return wire.Request{Topic: rule.DetailType, Headers: headers, Body: string(rule.Detail)}
}

// embeddedHeaders lifts the string-valued members of the EmbeddedHeadersKey object out of
// detail, when detail is a JSON object carrying one. A non-string value for a key is skipped
// (only string values are legal wire headers) rather than failing the whole event. Parsed
// generically (not via a struct tag - Go struct tags can't reference the EmbeddedHeadersKey
// constant) so the key lives in exactly one place.
func embeddedHeaders(detail json.RawMessage) map[string]string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(detail, &object); err != nil {
		return nil
	}
	rawEmbedded, ok := object[EmbeddedHeadersKey]
	if !ok {
		return nil
	}
	var embedded map[string]json.RawMessage
	if err := json.Unmarshal(rawEmbedded, &embedded); err != nil {
		return nil
	}
	headers := make(map[string]string, len(embedded))
	for k, raw := range embedded {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			headers[k] = value
		}
	}
	return headers
}

func addIfPresent(headers map[string]string, key, value string) {
	if value != "" {
		headers[key] = value
	}
}
