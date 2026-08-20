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
	"os"
	"strconv"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpstatus"
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
// by the FUNCTIONS_CUSTOMHANDLER_PORT environment variable, which the host sets - pass
// ListenAddr() as your server's Addr and the binding supplies it.
//
// Each Route's Path must match the *local* invocation path Azure uses for that function -
// by default "/<FunctionName>", the name of that function's folder (see its function.json) -
// which is independent of any public "route" property that function.json declares; Route.Path
// here is about the internal host<->handler contract, not the public URL. Route matching,
// including its "{version}" route-segment special case (versioning.md §2.1), is identical to
// httpbinding.Handler's.
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
		if version, ok := params["version"]; ok {
			topic = topic.WithVersion(version)
		}

		resp, _ := envelope.DispatchTopicResult(r.Context(), builder.Pipeline, builder.Container, topic, headers, trigger.Body)

		// The same §4.1 rendering httpbinding uses - in particular, a failure's problem document
		// reaches Outputs.res carrying the `status` member equal to the statusCode being sent,
		// which a response assembled by hand here used to omit while still calling itself
		// application/problem+json.
		code, respBody, respHeaders := httpstatus.Response(resp)
		writeInvocationResponse(w, code, respBody, respHeaders)
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

// The listen-address contract between the Azure Functions host and a custom handler. The host
// picks a free port, names it in FUNCTIONS_CUSTOMHANDLER_PORT, and starts the handler
// executable; a handler that binds anything else never receives an invocation. That is the
// binding's contract with its platform, not the service's business, so it lives here rather
// than being restated in every main().
const (
	// PortEnvVar is the environment variable the Functions host names the handler's listen
	// port in.
	PortEnvVar = "FUNCTIONS_CUSTOMHANDLER_PORT"
	// DefaultPort is the port ListenAddr falls back to when PortEnvVar is unset - running the
	// handler directly, outside the Functions host, to curl it by hand or in a test.
	DefaultPort = "8080"
)

// ListenAddr returns the address a custom handler should pass to http.ListenAndServe or
// http.Server.Addr: ":" + $FUNCTIONS_CUSTOMHANDLER_PORT, falling back to ":" + DefaultPort when
// the variable is unset (running outside the Functions host).
//
// It is a one-line shorthand composed from public API and the standard library, and the
// explicit form it composes is exactly:
//
//	port := os.Getenv(azurefunctions.PortEnvVar)
//	if port == "" {
//		port = azurefunctions.DefaultPort
//	}
//	addr := ":" + port
//
// Drop to that form to bind a specific interface, or to fail loudly rather than fall back when
// the host named no port. httpbinding.ListenAddr is the same shorthand for the PORT convention
// every other HTTP host uses.
func ListenAddr() string {
	port := os.Getenv(PortEnvVar)
	if port == "" {
		port = DefaultPort
	}
	return ":" + port
}
