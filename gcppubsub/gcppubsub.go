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
// The outbound half (publishing) is deliberately absent: Pub/Sub's Publish API needs
// OAuth-signed calls, i.e. the cloud.google.com/go/pubsub SDK - a dependency decision this
// repo hasn't taken (see ROADMAP.md). When it is, it gets its own module like awssqs/awssns.
package gcppubsub

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

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

// Handler adapts builder into the http.Handler a push subscription's endpoint URL points at.
// Each delivery gets its own pipeline invocation - its own DI scope, via envelope.Dispatch.
// Pub/Sub acknowledges on 102/200/201/202/204 and redelivers on anything else; this handler
// answers 204 for a success status, 500 (with the wire error payload as the body, for log
// inspection) for a non-success one, and 400 for a delivery that isn't a valid push envelope
// at all - also a nack, so a misconfigured subscription surfaces as redelivery/dead-letter
// traffic rather than silent loss.
func Handler(builder *benzene.ApplicationBuilder) http.Handler {
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

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, resolveRequest(push.Message, string(data)))
		if !benzene.Status(resp.StatusCode).IsSuccess() {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, resp.Body)
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
func resolveRequest(message pushMessage, data string) wire.Request {
	headers := make(map[string]string, len(message.Attributes))
	var topic string
	for name, value := range message.Attributes {
		if strings.EqualFold(name, "topic") {
			topic = value
			continue
		}
		headers[name] = value
	}

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
