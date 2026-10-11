// Package gcppubsub is the inbound half of the Google Cloud Pub/Sub binding: an HTTP handler
// for a push subscription (https://cloud.google.com/pubsub/docs/push), typically mounted on a
// Cloud Run service. It needs no third-party dependency - a push subscription delivers each
// message as a plain HTTPS POST of a small JSON envelope (base64 data + attributes), and
// acknowledgement is the response status code, so this is "just" JSON parsing, the same shape
// as awssqs's and awssns's inbound handlers.
//
// This is the concrete reason a dedicated Google Cloud package exists at all where plain
// Cloud Run needs none (see examples/gcp-cloudrun-helloworld/README.md): the push envelope's
// base64-encoded body, attribute map, and status-code acknowledgement contract are exactly
// the parts httpbinding.Handler cannot cover.
//
// This is transport-bindings.md's queue-shaped catalog entry applied to Pub/Sub: topic from
// the "topic" message attribute or the body parsed as a wire envelope, one DI scope per
// message. A successfully handled message is acknowledged with 204 No Content; a failed one
// is answered with 500, which Pub/Sub treats as a nack - the message is redelivered per the
// subscription's retry policy and eventually dead-lettered if one is configured, the Pub/Sub
// counterpart of awssqs's batch item failure and awssns's returned Go error.
//
// Settlement is per message (the "queue" shape of transport-bindings.md §1, obligation 8): a
// failure result, no result, a pipeline error and an unmatched topic are all nacked by default;
// WithSettlement offers the at-most-once override and unmatched-topic-as-ignored.
//
// The outbound half (publishing) needs OAuth-signed calls, i.e. the cloud.google.com/go/pubsub
// SDK, so it ships in its own module, gcppubsubclient, like awssqs/awssns.
package gcppubsub

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// pushRequest mirrors the fields this adapter needs from the push-subscription delivery
// envelope - see https://cloud.google.com/pubsub/docs/push#receive_push.
type pushRequest struct {
	Message pushMessage `json:"message"`
}

type pushMessage struct {
	// Data is the message payload, base64-encoded (protobuf's JSON mapping for bytes).
	Data       string            `json:"data"`
	Attributes map[string]string `json:"attributes"`
}

// Option configures Handler.
type Option func(*options)

type options struct {
	settlement envelope.Settlement
}

// WithSettlement sets how Handler settles a message whose invocation failed (transport-bindings.md §1,
// obligation 8). The default - the zero envelope.Settlement - nacks it (500) so Pub/Sub redelivers
// it per the subscription's retry policy; envelope.AtMostOnce acknowledges it (204) whatever the
// outcome, and IgnoreUnmatchedTopics acknowledges a message whose topic no handler serves - for a
// subscription that sees other services' topics.
func WithSettlement(settlement envelope.Settlement) Option {
	return func(o *options) { o.settlement = settlement }
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Handler adapts builder into the http.Handler a push subscription's endpoint URL points at.
// Each delivery gets its own pipeline invocation - its own DI scope. Pub/Sub acknowledges on
// 102/200/201/202/204 and redelivers on anything else; this handler answers 204 for a message the
// settlement acknowledges (by default, a successful one), 500 (with the wire error payload as the
// body, for log inspection) for one it does not, and 400 for a delivery that isn't a valid push
// envelope at all - also a nack, so a misconfigured subscription surfaces as redelivery/dead-letter
// traffic rather than silent loss.
func Handler(builder *benzene.ApplicationBuilder, opts ...Option) http.Handler {
	settlement := newOptions(opts).settlement
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var push pushRequest
		if err := json.Unmarshal(body, &push); err != nil {
			http.Error(w, "malformed push envelope: "+err.Error(), http.StatusBadRequest)
			return
		}

		data, err := base64.StdEncoding.DecodeString(push.Message.Data)
		if err != nil {
			http.Error(w, "malformed base64 message data", http.StatusBadRequest)
			return
		}

		outcome := settlement.Dispatch(r.Context(), builder.Pipeline, builder.Container, resolveRequest(push.Message, string(data), builder.ReservedNames.Topic()))
		if !outcome.Acknowledge {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, outcome.Response.Body)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// resolveRequest resolves the message's topic and headers per wire-contracts.md §2, the same
// order as awssqs/awssns: the "topic" message attribute (remaining attributes become
// headers), else the decoded data parsed as a full wire.Request envelope, else an empty
// topic, which RouterMiddleware maps to ValidationError - the message is nacked, never
// silently dropped.
func resolveRequest(message pushMessage, data, topicKey string) wire.Request {
	topic, headers := wire.ResolveMetadataTopic(message.Attributes, topicKey)

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: data}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal([]byte(data), &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}

	return wire.Request{Topic: "", Headers: headers, Body: data}
}
