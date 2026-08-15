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

// eventHubInvocationRequest is the custom-handler envelope for an Event Hub-triggered function
// configured with `"cardinality": "many"` (see EventHubHandler's doc comment for why this binding
// requires batch mode): Data carries the delivered batch of event bodies as a JSON array under the
// trigger binding's name (function.json's "name"). Metadata carries the host's batch-aligned,
// Array-suffixed trigger metadata - PartitionKeyArray, SequenceNumberArray, and so on - of which
// this binding reads only "PropertiesArray": the per-event application properties (Event Hubs'
// analogue of a Service Bus message's UserProperties, see queue.go), one map per event, in the same
// order as the event bodies.
type eventHubInvocationRequest struct {
	Data     map[string]json.RawMessage `json:"Data"`
	Metadata map[string]json.RawMessage `json:"Metadata"`
}

// EventHubHandler builds the HTTP server for an Azure Functions Event Hub custom-handler trigger
// (the Go counterpart of Benzene.Azure.Function.EventHub - benzene-dotnet's own README for
// examples/AzureFunctionsMesh notes this exact gap: "The Event Hub egress<->Functions-trigger round
// trip needed a small framework addition"). dataName is the trigger binding's "name" from that
// function's function.json; the batch is read from Data[dataName].
//
// # Batch mode only, following the .NET binding's own shape
//
// The Functions host offers two Event Hub trigger cardinalities: "one" (a single event per
// invocation, function.json's default) and "many" (a batch, function.json must opt in). .NET's own
// EventHubApplication is built exclusively around EventData[] - it always requires cardinality
// "many" - so this binding matches that shape rather than adding a second, single-event mode: the
// function.json for an EventHubHandler-mounted trigger must set "cardinality": "many".
//
// # One pipeline invocation per event, not one per batch
//
// Unlike CosmosHandler/TimerHandler (genuinely fan-in: the whole batch is one topic-routed
// invocation), an Event Hub batch is a batch of independently-topic-routed Benzene messages - each
// event carries its own topic in its application properties, exactly like a Service Bus message
// does on QueueHandler. So EventHubHandler dispatches each event in the batch as its own pipeline
// invocation (its own DI scope, via envelope.DispatchResult), resolving topic/headers/body per
// event with the exact same precedence QueueHandler uses for a Service Bus message:
//
//  1. a "topic" entry in that event's properties (PropertiesArray[i]) - the remaining string-valued
//     properties become headers, else
//  2. the event body parsed as a full wire.Request envelope, else
//  3. the event carries an empty topic, which RouterMiddleware maps to ValidationError - failed,
//     never silently dropped.
//
// # Ordering and redelivery: stop at the first failure
//
// Event Hubs delivers a partition's events in order and the Functions host checkpoints a batch
// trigger at the *invocation* level - there is no way to acknowledge part of a batch, so any
// failure anywhere in it redelivers the whole thing regardless of how the handler processes the
// rest. Given that, EventHubHandler dispatches events strictly in order and STOPS at the first
// non-success result rather than running the remaining events anyway: the same ordered-stream
// stop-at-first-failure stance azureeventhub.Consumer, awsdynamodb, and awskinesis already take in
// this port (see Consumer's own doc comment) - a deliberate divergence from .NET's
// EventHubApplication, which fans every event in the batch out concurrently and, by default
// (EventHubOptions.RaiseOnFailureStatus), only surfaces the failure once every event has run. Both
// shapes redeliver the same whole batch on failure; this one avoids running - and side-effecting -
// events downstream of one that is already going to be redelivered.
//
// A successful dispatch of every event answers the host with outer HTTP 200 (the host advances the
// partition's checkpoint past this batch); the first non-success dispatch answers outer HTTP 500
// (the host does not checkpoint, and the whole batch redelivers) - the same outer-status convention
// QueueHandler and CosmosHandler use.
//
// Mount it on the function's local invocation path alongside the other trigger handlers, e.g.
// mux.Handle("/OrderPlaced", azurefunctions.EventHubHandler(builder, "eventHubMessages")).
func EventHubHandler(builder *benzene.ApplicationBuilder, dataName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv eventHubInvocationRequest
		if err := json.Unmarshal(body, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		for _, req := range resolveEventHubBatch(inv, dataName) {
			resp, successful := envelope.DispatchResult(r.Context(), builder.Pipeline, builder.Container, req)
			if !successful {
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, resp.Body)
				return
			}
		}

		data, err := json.Marshal(queueSuccessResponse{Outputs: map[string]json.RawMessage{}})
		if err != nil {
			// queueSuccessResponse is an empty map - Marshal cannot fail on it in practice, but
			// degrade to a failed invocation (redelivery) rather than panic if it somehow ever does.
			http.Error(w, "failed to serialize invocation response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
}

// resolveEventHubBatch extracts the batch of per-event wire.Requests from Data[dataName], in
// delivery order. The host serializes the trigger input as JSON, so the batch arrives either as a
// JSON array directly or as a JSON *string* wrapping that array (the same double-encoding
// QueueHandler/CosmosHandler/TimerHandler handle); either way it is unwrapped to the raw array
// first. An absent binding name, or a present-but-null value, yields an empty batch (a no-op that
// checkpoints) rather than a failure - the host does not fire this trigger without events anyway.
func resolveEventHubBatch(inv eventHubInvocationRequest, dataName string) []wire.Request {
	raw, ok := inv.Data[dataName]
	if !ok {
		return nil
	}
	arrayJSON := raw
	var wrapped string
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		arrayJSON = json.RawMessage(wrapped)
	}
	if len(arrayJSON) == 0 || string(arrayJSON) == "null" {
		return nil
	}

	var bodies []json.RawMessage
	if err := json.Unmarshal(arrayJSON, &bodies); err != nil {
		return nil
	}

	properties := resolveEventHubProperties(inv, len(bodies))
	requests := make([]wire.Request, len(bodies))
	for i, eventBody := range bodies {
		requests[i] = resolveEventHubRequest(eventBody, properties[i])
	}
	return requests
}

// resolveEventHubProperties reads Metadata["PropertiesArray"] - the batch-aligned per-event
// application properties - into a slice of length n, one entry per event. A missing/malformed
// PropertiesArray, or one shorter than the batch, degrades to nil (no properties) for the events it
// does not cover, rather than failing the whole batch: a nil map in resolveEventHubRequest simply
// means that event falls through to its envelope-in-body/empty-topic resolution.
func resolveEventHubProperties(inv eventHubInvocationRequest, n int) []map[string]json.RawMessage {
	out := make([]map[string]json.RawMessage, n)
	raw, ok := inv.Metadata["PropertiesArray"]
	if !ok {
		return out
	}
	var parsed []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out
	}
	copy(out, parsed)
	return out
}

// resolveEventHubRequest resolves one event's topic/headers/body per the precedence documented on
// EventHubHandler. The event body is read the same way QueueHandler reads a message: the host
// serializes each array element as JSON, so a text event arrives as a JSON string (unquoted here)
// and a JSON event may arrive as an object (kept verbatim as the body).
func resolveEventHubRequest(eventBody json.RawMessage, properties map[string]json.RawMessage) wire.Request {
	var messageBody string
	if err := json.Unmarshal(eventBody, &messageBody); err != nil {
		messageBody = string(eventBody)
	}

	headers := map[string]string{}
	var topic string
	// Application properties are typed (string/number/bool); only string values map onto the
	// flat wire header contract, so non-string values are skipped rather than failing the event.
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
