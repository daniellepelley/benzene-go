package meshapp

// This file reimplements the two small pieces of the Azure Functions custom-handler HTTP
// contract that azurefunctions.Handler's fixed Route-per-topic table cannot express:
//
//   - the wire-envelope endpoint (httpbinding.EnvelopePath, "POST /benzene/invoke"): its topic is
//     resolved from the request BODY, not from a fixed (method, path) pair, so it cannot be
//     expressed as a httpbinding.Route the way /benzene/spec or /benzene/health can (see
//     meshapp.go's package doc);
//   - wrapping an ordinary net/http.Handler that expects a real *http.Request/http.ResponseWriter
//     (meshd.Collector.ViewHandler, which serves the mesh Function's Fleet View as static
//     HTML+JS) onto that same custom-handler contract.
//
// Both are EXAMPLE-LOCAL glue, not framework code: azurefunctions.go's own invocationRequest/
// httpTriggerData/writeInvocationResponse types are unexported package-internal decode details of
// azurefunctions.Handler's Route-table dispatch, so this file re-declares the small subset it
// needs, the same move examples/aws-lambda-mesh/cmd/mesh/main.go's own dispatchEnvelopeOverHTTP
// already makes for the equivalent Lambda HTTP event shape (see that file's package doc). This
// repo's own verification notes confirm azurefunctions.EventHubHandler is the only change made to
// the azurefunctions package for this example - everything here stays inside
// examples/azure-functions-mesh.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// httpTriggerInvocation is a local reimplementation of the custom-handler HTTP trigger payload
// documented on azurefunctions.Handler ({"Data":{"req":{"Method","Headers","Body"}}}, the trigger
// binding's "name" being "req" by this repo's own convention - see every function.json in
// cmd/*/Spec, Health, Invoke, and each service's native route).
type httpTriggerInvocation struct {
	Data struct {
		Req struct {
			Method  string            `json:"Method"`
			Headers map[string]string `json:"Headers"`
			Body    string            `json:"Body"`
		} `json:"req"`
	} `json:"Data"`
}

// invocationResponse/httpOutputBinding mirror the custom-handler response contract
// azurefunctions.go's own (unexported) types encode: outer HTTP 200, the real outcome carried in
// Outputs.res.
type invocationResponse struct {
	Outputs map[string]httpOutputBinding `json:"Outputs"`
}

type httpOutputBinding struct {
	StatusCode string            `json:"statusCode"`
	Body       string            `json:"body"`
	Headers    map[string]string `json:"headers,omitempty"`
}

// WriteInvocationResponse wraps (statusCode, body, headers) in the custom-handler response
// envelope and writes it with outer HTTP 200 - the Functions host's own convention (a non-200
// outer status here means the custom handler process itself failed, not an application outcome).
// Exported so cmd/mesh/main.go's small "/mesh/discovered" convenience handler (an in-process
// envelope.DispatchTopicResult call, not a raw request/response round trip) can answer through
// the same contract without duplicating this wrapping.
func WriteInvocationResponse(w http.ResponseWriter, statusCode int, body string, headers map[string]string) {
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

// EnvelopeHandler builds the Azure Functions custom-handler surface for httpbinding.EnvelopePath
// (POST /benzene/invoke) over builder. Every Function in this estate mounts it at the "Invoke"
// local path: the six services for Cloud Service Profile compliance (this topology's own domain
// hops travel over Service Bus/Event Hub/Event Grid, never HTTP), and the mesh Function
// load-bearingly - it is what the six services push register/heartbeat/trace/issue reports to
// (see cmd/mesh/main.go) and what the Fleet View's own same-origin JS polls for
// benzene:mesh:query:fleet. Exported (rather than only reachable via App.EnvelopeHandler) because
// cmd/mesh/main.go wraps meshd.Collector.Builder() directly, with no App of its own - the mesh
// Function is a thin wrapper around the collector, not a composite service, matching
// examples/k8s-mesh-helloworld/cmd/mesh and examples/aws-lambda-mesh/cmd/mesh's own shape.
func EnvelopeHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv httpTriggerInvocation
		if err := json.Unmarshal(raw, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		wireReq, err := wire.UnmarshalRequest([]byte(inv.Data.Req.Body))
		if err != nil {
			http.Error(w, "malformed envelope: "+err.Error(), http.StatusBadRequest)
			return
		}

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, wireReq)
		data, err := wire.MarshalResponse(resp)
		if err != nil {
			http.Error(w, "failed to serialize envelope response", http.StatusInternalServerError)
			return
		}

		// Outer HTTP 200 always - the real Benzene outcome travels inside the envelope's own
		// statusCode field, matching httpbinding.EnvelopeHandler's own convention exactly.
		WriteInvocationResponse(w, http.StatusOK, string(data), map[string]string{"content-type": "application/json"})
	})
}

// WrapRawHandler adapts an ordinary net/http.Handler expecting a real *http.Request/
// http.ResponseWriter (meshd.Collector.ViewHandler, serving the Fleet View's static HTML+JS) onto
// the custom-handler contract: it rebuilds a synthetic request from the invocation's "req"
// trigger data, captures h's response with an httptest.ResponseRecorder, and wraps the result -
// the same httptest.NewRecorder round trip examples/aws-lambda-mesh/cmd/mesh/main.go's own
// newMeshHandler already uses for the equivalent Lambda HTTP event case. Only the mesh Function's
// "FleetUi" local path needs this; every other HTTP surface in this estate is either
// azurefunctions.Handler's Route-table dispatch or newEnvelopeHandler above.
func WrapRawHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		var inv httpTriggerInvocation
		if err := json.Unmarshal(raw, &inv); err != nil {
			http.Error(w, "malformed invocation payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		method := inv.Data.Req.Method
		if method == "" {
			method = http.MethodGet
		}
		inner := httptest.NewRequest(method, "/", strings.NewReader(inv.Data.Req.Body))
		for name, value := range inv.Data.Req.Headers {
			inner.Header.Set(name, value)
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, inner)

		WriteInvocationResponse(w, rec.Code, rec.Body.String(), flattenHeader(rec.Header()))
	})
}

// flattenHeader lower-cases header names, matching wire-contracts.md §2 ("SHOULD be written
// lower-case") and httpbinding.headersFrom's own convention.
func flattenHeader(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for name := range h {
		out[strings.ToLower(name)] = h.Get(name)
	}
	return out
}
