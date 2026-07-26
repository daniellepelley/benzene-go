package azurefunctions

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// queueInvocationRequest is the custom-handler envelope for a queue-shaped trigger: Data
// carries the message under the trigger binding's name (function.json's "name"), Metadata
// carries the platform metadata - for Service Bus that includes UserProperties, the
// application-properties map that plays the role SQS/SNS message attributes play in
// wire-contracts.md §2.
type queueInvocationRequest struct {
	Data     map[string]json.RawMessage `json:"Data"`
	Metadata map[string]json.RawMessage `json:"Metadata"`
}

// queueSuccessResponse is the reply for a successfully handled message: outer HTTP 200 with
// no output bindings (the pipeline's result has nowhere to go on a fire-and-forget trigger -
// a non-success result is reported via the outer status instead, see QueueHandler).
type queueSuccessResponse struct {
	Outputs map[string]json.RawMessage `json:"Outputs"`
}

// QueueHandler builds the HTTP server for queue-shaped custom-handler triggers - Azure
// Storage Queue and Service Bus queue/topic triggers, which share the same Data/Metadata
// invocation envelope. dataName is the trigger binding's "name" from that function's
// function.json (e.g. "queueItem" for a queue trigger, "mySbMsg" for a Service Bus trigger);
// the message is read from Data[dataName].
//
// Topic resolution follows wire-contracts.md §2, the same order as awssqs/awssns:
//
//  1. a "topic" entry in Metadata.UserProperties (Service Bus application properties - the
//     native-attribute transport; the remaining properties become headers), else
//  2. the message body parsed as a full wire.Request envelope (the only option on Azure
//     Storage Queues, which have no per-message attributes), else
//  3. the request carries an empty topic, which RouterMiddleware maps to ValidationError -
//     the message is failed, never silently dropped.
//
// Unlike the HTTP Handler's outer-200 convention, a non-success dispatch here answers the
// host with outer HTTP 500: on a queue-shaped trigger a non-2xx custom-handler response is
// how the invocation is marked failed, which hands the message to the platform's own retry
// machinery (Storage Queue redelivery up to maxDequeueCount then the poison queue; Service
// Bus abandon/redelivery then the dead-letter queue) - the Azure counterpart of awssns's
// returned Go error and awssqs's batch item failure.
//
// A single custom handler hosting several trigger types mounts one handler per function
// path on a mux, e.g. mux.Handle("/GreetQueue", azurefunctions.QueueHandler(builder,
// "queueItem")) alongside Handler for the HTTP-triggered functions.
func QueueHandler(builder *benzene.ApplicationBuilder, dataName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv queueInvocationRequest
		if err := json.Unmarshal(body, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, resolveQueueRequest(inv, dataName))
		if !benzene.Status(resp.StatusCode).IsSuccess() {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, resp.Body)
			return
		}

		data, err := json.Marshal(queueSuccessResponse{Outputs: map[string]json.RawMessage{}})
		if err != nil {
			// queueSuccessResponse is an empty map - Marshal cannot fail on it in practice, but
			// degrade to a failed invocation (redelivery) rather than panic if it somehow ever
			// does.
			http.Error(w, "failed to serialize invocation response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
}

// resolveQueueRequest resolves the message's topic/headers/body per the order documented on
// QueueHandler. The message text is Data[dataName]: the host serializes the trigger input as
// JSON, so a text message arrives as a JSON string (unquoted here) and a JSON message may
// arrive as an object (kept verbatim as the body).
func resolveQueueRequest(inv queueInvocationRequest, dataName string) wire.Request {
	var messageBody string
	if raw, ok := inv.Data[dataName]; ok {
		if err := json.Unmarshal(raw, &messageBody); err != nil {
			messageBody = string(raw)
		}
	}

	headers := map[string]string{}
	var topic string
	if raw, ok := inv.Metadata["UserProperties"]; ok {
		// Application properties are typed (string/number/bool); only string values map onto
		// the flat wire header contract, so non-string values are skipped rather than failing
		// the whole property map.
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(raw, &properties); err == nil {
			for name, rawValue := range properties {
				var value string
				if err := json.Unmarshal(rawValue, &value); err != nil {
					continue
				}
				if strings.EqualFold(name, "topic") {
					topic = value
					continue
				}
				headers[name] = value
			}
		}
	}

	if topic != "" {
		return wire.Request{Topic: topic, Headers: headers, Body: messageBody}
	}

	var envelopeReq wire.Request
	if err := json.Unmarshal([]byte(messageBody), &envelopeReq); err == nil && envelopeReq.Topic != "" {
		for k, v := range envelopeReq.Headers {
			headers[k] = v
		}
		return wire.Request{Topic: envelopeReq.Topic, Headers: headers, Body: envelopeReq.Body}
	}

	return wire.Request{Topic: "", Headers: headers, Body: messageBody}
}
