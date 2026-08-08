package azurefunctions

import (
	"encoding/json"
	"io"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// eventGridInvocationRequest is the custom-handler envelope for an Event Grid trigger: Data
// carries the delivered event under the trigger binding's name (function.json's "name"). There is
// no per-message header channel (unlike a Service Bus trigger's Metadata.UserProperties) - the
// event's own envelope fields become the headers, see resolveEventGridRequest.
type eventGridInvocationRequest struct {
	Data map[string]json.RawMessage `json:"Data"`
}

// EventGridHandler builds the HTTP server for an Azure Functions Event Grid custom-handler trigger.
// dataName is the trigger binding's "name" from that function's function.json (e.g. "eventGridEvent");
// the event is read from Data[dataName]. Matches Benzene.Azure.Function.EventGrid.
//
// One event per invocation: the Functions host de-batches an Event Grid delivery into one invocation
// per event (an EventGridTrigger binds a single event), so this is a single-message trigger like
// QueueHandler, not a fan-in like CosmosHandler.
//
// The event is mapped to a Benzene message the same way the .NET binding does:
//
//   - Topic: the event TYPE - Event Grid schema's "eventType" or CloudEvents 1.0's "type" (e.g.
//     "Microsoft.Storage.BlobCreated", or your own custom type), so an event routes to the handler
//     declaring that topic. The two wire schemas are told apart by the presence of "specversion"
//     (CloudEvents). This mirrors awss3 routing on the S3 event name.
//   - Body: the event's "data" payload as raw JSON, or "{}" when the event carries none (so a
//     handler with an empty request type still binds).
//   - Headers: the event's envelope fields - "id", "subject", and "source" (the full resource path
//     of the event source: Event Grid schema's "topic" / CloudEvents' "source", renamed to "source"
//     so it does not collide with Benzene's own routing notion of topic). Only the present ones.
//
// Event Grid deliveries are fire-and-forget from the handler's perspective - Event Grid's own
// retry + dead-letter machinery is what a failure feeds - so, like QueueHandler, a non-success
// dispatch answers outer HTTP 500 (the only ack channel a custom handler has) and a success answers
// outer HTTP 200. A missing/unparseable event resolves to an empty topic, which RouterMiddleware
// maps to ValidationError (failed, so outer 500) - never a silent drop.
//
// Mount it per function path alongside the other trigger handlers, e.g.
// mux.Handle("/BlobCreated", azurefunctions.EventGridHandler(builder, "eventGridEvent")).
func EventGridHandler(builder *benzene.ApplicationBuilder, dataName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv eventGridInvocationRequest
		if err := json.Unmarshal(body, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		resp, successful := envelope.DispatchResult(r.Context(), builder.Pipeline, builder.Container, resolveEventGridRequest(inv, dataName))
		if !successful {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, resp.Body)
			return
		}

		data, err := json.Marshal(queueSuccessResponse{Outputs: map[string]json.RawMessage{}})
		if err != nil {
			// An empty Outputs map cannot fail to marshal in practice; degrade to a failed
			// invocation (Event Grid retry) rather than panic if it somehow ever does.
			http.Error(w, "failed to serialize invocation response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
}

// resolveEventGridRequest pulls the event out of Data[dataName] and maps it to a wire.Request. The
// host may deliver the event as a JSON string wrapping the object (the double-encoding the other
// triggers apply to a trigger input) or as the object directly; a string is unwrapped first.
func resolveEventGridRequest(inv eventGridInvocationRequest, dataName string) wire.Request {
	raw, ok := inv.Data[dataName]
	if !ok {
		return wire.Request{}
	}
	eventJSON := raw
	var wrapped string
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		eventJSON = json.RawMessage(wrapped)
	}
	return parseEventGridEvent(eventJSON)
}

// eventGridEvent captures both wire schemas Event Grid can deliver. The Event Grid schema uses
// eventType/topic; CloudEvents 1.0 uses type/source and is detected by specversion. The fields for
// both are parsed and selected in parseEventGridEvent.
type eventGridEvent struct {
	SpecVersion *string         `json:"specversion"`
	ID          string          `json:"id"`
	EventType   string          `json:"eventType"` // Event Grid schema
	Type        string          `json:"type"`      // CloudEvents
	Subject     string          `json:"subject"`
	Topic       string          `json:"topic"`  // Event Grid schema source
	Source      string          `json:"source"` // CloudEvents source
	Data        json.RawMessage `json:"data"`
}

// parseEventGridEvent maps one Event Grid / CloudEvents event to a wire.Request. An unparseable
// event yields an empty topic (RouterMiddleware -> ValidationError), so it is failed, not dropped.
func parseEventGridEvent(eventJSON json.RawMessage) wire.Request {
	var event eventGridEvent
	if err := json.Unmarshal(eventJSON, &event); err != nil {
		return wire.Request{}
	}

	topic := event.EventType
	source := event.Topic
	if event.SpecVersion != nil { // CloudEvents 1.0
		topic = event.Type
		source = event.Source
	}

	headers := map[string]string{}
	if event.ID != "" {
		headers["id"] = event.ID
	}
	if event.Subject != "" {
		headers["subject"] = event.Subject
	}
	if source != "" {
		headers["source"] = source
	}

	body := "{}"
	if len(event.Data) > 0 && string(event.Data) != "null" {
		body = string(event.Data)
	}

	return wire.Request{Topic: topic, Headers: headers, Body: body}
}
