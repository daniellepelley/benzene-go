package azurefunctions

import (
	"encoding/json"
	"io"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
)

// timerInvocationRequest is the custom-handler envelope for a Timer trigger: the tick's schedule
// info arrives under the trigger binding's name in Data (a JSON object). There is no Metadata worth
// reading - a timer tick carries no per-message metadata.
type timerInvocationRequest struct {
	Data map[string]json.RawMessage `json:"Data"`
}

// TimerHandler builds the HTTP server for an Azure Functions Timer trigger (matching
// Benzene.Azure.Function.Timer). Like CosmosHandler it is **fan-in, not topic-routed**: a timer tick
// carries no message, so the topic is the scheduled job's identity, named here by the developer
// rather than parsed from the tick. Each tick is one pipeline invocation dispatched to topic, with
// the tick's schedule info (IsPastDue, ScheduleStatus) as the body - so a handler whose request
// mirrors that receives the schedule, and a handler with an empty request (struct{}) binds cleanly
// too. dataName is the trigger binding's function.json "name" (e.g. "myTimer").
//
// A tick has no caller to answer, so the outer status is for the service's own logging/monitoring: a
// successful dispatch answers outer HTTP 200, a non-success outer HTTP 500. A timer trigger has no
// redelivery (unlike a queue or change feed), so the 500 does not cause a retry - it simply surfaces
// the failed run to the Functions host's monitoring, keeping the same outer-status convention as the
// other queue-shaped handlers.
//
// Mount it on the function's local invocation path alongside the other trigger handlers, e.g.
// mux.Handle("/NightlyCleanup", azurefunctions.TimerHandler(builder, benzene.NewTopic("nightly-cleanup"), "myTimer")).
func TimerHandler(builder *benzene.ApplicationBuilder, topic benzene.Topic, dataName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv timerInvocationRequest
		if err := json.Unmarshal(body, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		resp, successful := envelope.DispatchTopicResult(r.Context(), builder.Pipeline, builder.Container, topic, nil, resolveTimerBody(inv, dataName))
		if !successful {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, resp.Body)
			return
		}

		data, err := json.Marshal(queueSuccessResponse{Outputs: map[string]json.RawMessage{}})
		if err != nil {
			// queueSuccessResponse is an empty map - Marshal cannot fail on it in practice, but
			// degrade to a failed invocation rather than panic if it somehow ever does.
			http.Error(w, "failed to serialize invocation response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
}

// resolveTimerBody extracts the tick's schedule-info body from Data[dataName]. The host serializes
// the trigger input as JSON, so it arrives either as a JSON object directly or as a JSON string
// wrapping that object (the same double-encoding QueueHandler/CosmosHandler handle); either way the
// body ends up as the raw object JSON. An absent binding name, or a null value, yields "{}" so a
// handler with an empty request type still binds cleanly.
func resolveTimerBody(inv timerInvocationRequest, dataName string) string {
	body := "{}"
	if raw, ok := inv.Data[dataName]; ok {
		var unwrapped string
		if err := json.Unmarshal(raw, &unwrapped); err == nil {
			body = unwrapped
		} else {
			body = string(raw)
		}
	}
	if body == "" || body == "null" {
		body = "{}"
	}
	return body
}
