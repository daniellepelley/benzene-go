// Package awssns is the AWS SNS binding: a Lambda function subscribed directly to an SNS
// topic (inbound, zero new dependency - the same shape as awssqs's own inbound handler, since
// AWS delivers the notification as plain JSON to the invocation) plus an outbound publish
// client (needs aws-sdk-go-v2/service/sns - see client.go).
//
// Unlike SQS's event source mapping, a direct SNS-to-Lambda subscription has no batch or
// partial-failure concept: each notification is its own asynchronous Lambda invocation, and
// AWS's own async-invoke retry (then, if configured, a dead-letter queue) is what handles a
// failure - triggered by this package returning a Go error, not a special response body.
package awssns

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// snsEvent mirrors the fields this adapter needs from the Lambda SNS invocation payload - see
// https://docs.aws.amazon.com/sns/latest/dg/sns-message-and-json-formats.html. AWS invokes
// Lambda once per notification, so Records normally holds exactly one entry; this adapter
// still loops (rather than indexing Records[0]) so it degrades gracefully rather than
// panicking if that ever isn't true.
type snsEvent struct {
	Records []snsRecord `json:"Records"`
}

type snsRecord struct {
	Sns snsMessage `json:"Sns"`
}

type snsMessage struct {
	MessageID         string                         `json:"MessageId"`
	Message           string                         `json:"Message"`
	MessageAttributes map[string]snsMessageAttribute `json:"MessageAttributes"`
}

type snsMessageAttribute struct {
	Value string `json:"Value"`
}

// Handler adapts builder into an awslambda.HandlerFunc for a Lambda subscribed directly to an
// SNS topic. Every notification runs through the pipeline with its own DI scope, via
// envelope.Dispatch. If any notification's dispatch result is not a success status, Handler
// returns a Go error summarizing which - triggering AWS's own async-invoke retry - rather than
// a partial-failure response body, since SNS has no such mechanism to report one to.
func Handler(builder *benzene.ApplicationBuilder) awslambda.HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var snsEvt snsEvent
		if err := json.Unmarshal(event, &snsEvt); err != nil {
			return nil, fmt.Errorf("awssns: malformed SNS event: %w", err)
		}

		var failures []string
		for _, record := range snsEvt.Records {
			req := resolveRequest(record.Sns)
			resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, req)
			if !benzene.Status(resp.StatusCode).IsSuccess() {
				failures = append(failures, fmt.Sprintf("%s: %s", record.Sns.MessageID, resp.StatusCode))
			}
		}

		if len(failures) > 0 {
			return nil, fmt.Errorf("awssns: %d of %d notification(s) failed: %s", len(failures), len(snsEvt.Records), strings.Join(failures, "; "))
		}
		return json.Marshal(struct{}{})
	}
}

// resolveRequest resolves a notification's topic and headers per wire-contracts.md §2, the
// same convention awssqs's inbound handler reads: a message attribute named "topic" when
// present; otherwise the notification's Message is instead parsed as a full wire.Request
// envelope (topic/headers/body) - the "or envelope" fallback. If neither yields a topic, the
// request carries an empty topic, which RouterMiddleware maps to ValidationError (router.go),
// surfaced as a Go error (see Handler) rather than a silently dropped notification.
func resolveRequest(sns snsMessage) wire.Request {
	headers := make(map[string]string, len(sns.MessageAttributes))
	var topic string
	for name, attr := range sns.MessageAttributes {
		if strings.EqualFold(name, "topic") {
			topic = attr.Value
			continue
		}
		headers[name] = attr.Value
	}

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: sns.Message}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal([]byte(sns.Message), &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}

	return wire.Request{Topic: "", Headers: headers, Body: sns.Message}
}
