package azurefunctions

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
)

// cosmosInvocationRequest is the custom-handler envelope for a Cosmos DB Change Feed trigger. The
// Functions host owns the change-feed connection and lease container; it forwards each delivered
// batch of changed documents under the trigger binding's name in Data (a JSON array), so the
// custom handler is zero-dependency - it never opens a Cosmos connection itself. Metadata is not
// used: unlike a queue-shaped trigger, a change-feed delivery carries no per-message application
// properties - the changed documents *are* the payload.
type cosmosInvocationRequest struct {
	Data map[string]json.RawMessage `json:"Data"`
}

// CosmosHandler builds the HTTP server for a Cosmos DB Change Feed trigger (the
// Benzene.Azure.Function.CosmosDb flavor of transport-bindings.md's "Cosmos DB Change Feed"
// entry). Unlike the topic-routed bindings, this one is **fan-in, not topic-routed**
// (core-concepts.md §3, streaming-shaped): the whole delivered batch of changed documents is one
// pipeline invocation - not one per document - dispatched to the single topic the developer names
// here, whose handler receives the batch as its request (idiomatically a slice,
// benzene.Handler[[]TDocument, TRes]). dataName is the trigger binding's "name" from that
// function's function.json (e.g. "documents"); the batch is read from Data[dataName].
//
// Checkpointing is the Functions host's job and is batch-level - the change feed has no
// per-document resume token - and it happens on a successful return only. So this mirrors
// QueueHandler's outer-status convention exactly: a successful dispatch answers outer HTTP 200
// (the host advances the lease past this batch), and any non-success dispatch answers outer HTTP
// 500 (the host does not checkpoint, and redelivers the entire batch - there is no partial
// checkpoint, so the handler must be idempotent across a redelivered batch, same as the self-
// hosted worker).
//
// Mount it on the function's local invocation path alongside the other trigger handlers, e.g.
// mux.Handle("/OrdersChanged", azurefunctions.CosmosHandler(builder, benzene.NewTopic("orders:changed"), "documents")).
func CosmosHandler(builder *benzene.ApplicationBuilder, topic benzene.Topic, dataName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv cosmosInvocationRequest
		if err := json.Unmarshal(body, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		headers, batch := resolveCosmosBatch(inv, dataName)
		resp, successful := envelope.DispatchTopicResult(r.Context(), builder.Pipeline, builder.Container, topic, headers, batch)
		if !successful {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, resp.Body)
			return
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

// resolveCosmosBatch extracts the fan-in batch for a change-feed delivery: the batch of changed
// documents as the body (a JSON array) plus a cosmos-document-count header. The host serializes
// the trigger input as JSON, so the batch arrives either as a JSON array directly or as a JSON
// *string* wrapping that array (the same double-encoding QueueHandler handles); either way the
// body ends up as the raw array JSON so a handler deserializes []TDocument. An absent binding name
// yields an empty batch ("[]") rather than a failure - the fan-in handler decides what an empty
// batch means, and the host does not fire this trigger without changes anyway.
func resolveCosmosBatch(inv cosmosInvocationRequest, dataName string) (map[string]string, string) {
	body := "[]"
	if raw, ok := inv.Data[dataName]; ok {
		var unwrapped string
		if err := json.Unmarshal(raw, &unwrapped); err == nil {
			body = unwrapped
		} else {
			body = string(raw)
		}
	}

	headers := map[string]string{}
	// A document count is useful to logging/middleware without re-parsing the batch; skip it
	// (rather than fail the batch) if the body is not a JSON array.
	var documents []json.RawMessage
	if err := json.Unmarshal([]byte(body), &documents); err == nil {
		headers["cosmos-document-count"] = strconv.Itoa(len(documents))
	}

	return headers, body
}
