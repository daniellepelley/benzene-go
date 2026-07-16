// Package azurefunctions is the Azure Functions custom-handler binding
// (https://learn.microsoft.com/azure/azure-functions/functions-custom-handlers): Azure has no
// native Go worker, so a Go function ships as a plain HTTP server that the Functions host
// forwards each invocation to, over a small JSON envelope (Data/Metadata in,
// Outputs/ReturnValue out) - a "raw HTTP request/response" contract in spirit, close enough to
// transport-bindings.md's HTTP binding entry that Handler here mirrors httpbinding.Handler's
// shape (an explicit Route table, real HTTP status codes) rather than inventing a new one.
//
// This implements the *default* custom-handler payload. Handler covers HTTP-triggered
// functions; QueueHandler (queue.go) covers queue-shaped triggers (Azure Storage Queue and
// Service Bus). Other trigger types (Timer, Blob, ...) are not implemented - they follow the
// same Data/Metadata envelope, so a new adapter is the QueueHandler pattern with a different
// payload interpretation. Setting host.json's customHandler.enableForwardingHttpRequest to true
// switches Azure to forward the raw HTTP request/response instead of this JSON envelope; in
// that mode, skip this package and pass httpbinding.Handler straight to http.ListenAndServe -
// see examples/azure-functions-helloworld's README for the tradeoff between the two modes.
package azurefunctions

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpstatus"
	"github.com/daniellepelley/benzene-go/wire"
)

// invocationRequest is the JSON body the Functions host POSTs to the custom handler per
// invocation.
type invocationRequest struct {
	Data map[string]json.RawMessage `json:"Data"`
}

// httpTriggerData is the "req" entry inside Data for an HTTP-triggered function.
type httpTriggerData struct {
	Method  string            `json:"Method"`
	Headers map[string]string `json:"Headers"`
	Body    string            `json:"Body"`
}

// invocationResponse is the JSON body the custom handler must respond with.
type invocationResponse struct {
	Outputs map[string]httpOutputBinding `json:"Outputs"`
}

type httpOutputBinding struct {
	StatusCode string            `json:"statusCode"`
	Body       string            `json:"body"`
	Headers    map[string]string `json:"headers,omitempty"`
}

// Handler builds the HTTP server the Functions host invokes
// (host.json's customHandler.description.defaultExecutablePath), listening on the port named
// by the FUNCTIONS_CUSTOMHANDLER_PORT environment variable (set by the host - read it in
// main, same as httpbinding's examples read PORT).
//
// Each Route's Path must match the *local* invocation path Azure uses for that function -
// by default "/<FunctionName>", the name of that function's folder (see its function.json) -
// which is independent of any public "route" property that function.json declares; Route.Path
// here is about the internal host<->handler contract, not the public URL.
func Handler(builder *benzene.ApplicationBuilder, routes []httpbinding.Route) http.Handler {
	table := httpbinding.NewRouteTable(routes)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv invocationRequest
		if err := json.Unmarshal(body, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		var trigger httpTriggerData
		if raw, ok := inv.Data["req"]; ok {
			if err := json.Unmarshal(raw, &trigger); err != nil {
				http.Error(w, "malformed http trigger data: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		topic, params, ok := table.Match(trigger.Method, r.URL.Path)
		if !ok {
			writeInvocationResponse(w, http.StatusNotFound, "not found", nil)
			return
		}

		headers := trigger.Headers
		if headers == nil && len(params) > 0 {
			headers = make(map[string]string, len(params))
		}
		for name, value := range params {
			headers["route-"+name] = value
		}

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, wire.Request{
			Topic:   topic.String(),
			Headers: headers,
			Body:    trigger.Body,
		})

		writeInvocationResponse(w, httpstatus.ToHTTP(benzene.Status(resp.StatusCode)), resp.Body, resp.Headers)
	})
}

// writeInvocationResponse always answers the Functions host with outer HTTP 200 - the real
// result travels in Outputs.res.statusCode, matching how the host itself interprets a custom
// handler's response (a non-200 *outer* status here would be treated as the custom handler
// process itself failing, not an application-level outcome).
func writeInvocationResponse(w http.ResponseWriter, statusCode int, body string, headers map[string]string) {
	inv := invocationResponse{Outputs: map[string]httpOutputBinding{
		"res": {StatusCode: strconv.Itoa(statusCode), Body: body, Headers: headers},
	}}
	data, err := json.Marshal(inv)
	if err != nil {
		// invocationResponse is plain strings/maps of strings - Marshal cannot fail on it in
		// practice, but degrade gracefully rather than panic if it somehow ever does.
		http.Error(w, "failed to serialize invocation response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
