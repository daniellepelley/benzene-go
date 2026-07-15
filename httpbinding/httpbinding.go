// Package httpbinding is the HTTP transport binding described by
// daniellepelley/Benzene's docs/specification/transport-bindings.md §2 ("HTTP (ASP.NET Core)"
// entry, ported to Go's net/http): topic resolved from route/method conventions, headers both
// directions, status via httpstatus's wire-contracts.md §4.1 table, one DI scope per request,
// cancellation from the request's context.
//
// It offers two entry points:
//   - Handler: a native REST-style handler - real HTTP status codes, an explicit route table.
//   - EnvelopeHandler: the wire-contracts.md §1 envelope carried directly over HTTP (always
//     HTTP 200; the real outcome travels in the envelope's own statusCode field) - the "raw
//     BenzeneMessage envelope for direct invocation" transport-bindings.md's Lambda catalog
//     entry describes, useful for service-to-service calls with no route table to agree on.
package httpbinding

import (
	"io"
	"net/http"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpstatus"
	"github.com/daniellepelley/benzene-go/wire"
)

// Route maps one (method, path) pair to a topic - the routing rule transport-bindings.md §1.2
// calls for. Path matching is an exact string match against r.URL.Path; this MVP has no
// path-parameter templating (see README's scope note).
type Route struct {
	Method string
	Path   string
	Topic  benzene.Topic
}

// Handler builds a native HTTP entry point: an incoming request is matched against routes,
// dispatched through builder's pipeline (via envelope.Dispatch), and the result mapped back to
// a real HTTP status code (httpstatus.ToHTTP) and JSON body.
//
// Scope: one DI scope per request (transport-bindings.md §1.6). Cancellation: the request's
// own context (its Done channel fires on client disconnect or server shutdown) - core-concepts
// §4's "no cancellation parameter on the pipeline signature, it rides on ctx" rule.
//
// Response headers: not yet supported beyond content-type - InvocationContext carries no
// outbound header slot in this MVP (see README's scope note).
func Handler(builder *benzene.ApplicationBuilder, routes []Route) http.Handler {
	byKey := make(map[string]benzene.Topic, len(routes))
	for _, route := range routes {
		byKey[routeKey(route.Method, route.Path)] = route.Topic
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		topic, ok := byKey[routeKey(r.Method, r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, wire.Request{
			Topic:   topic.String(),
			Headers: headersFrom(r.Header),
			Body:    string(body),
		})
		writeNativeResponse(w, resp)
	})
}

// EnvelopeHandler builds an HTTP entry point that speaks the wire-contracts.md §1 envelope
// directly: the request body is a wire.Request (any method/path - the caller POSTs the
// envelope), and the response body is a wire.Response. The outer HTTP status is always 200;
// the real Benzene outcome travels inside the envelope's own statusCode field, matching how a
// Lambda invoke or an internal function call carries the envelope with no HTTP layer at all.
func EnvelopeHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		req, err := wire.UnmarshalRequest(body)
		if err != nil {
			http.Error(w, "malformed envelope: "+err.Error(), http.StatusBadRequest)
			return
		}

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, req)
		data, err := wire.MarshalResponse(resp)
		if err != nil {
			http.Error(w, "failed to serialize envelope response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})
}

func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}

// headersFrom flattens net/http's multi-value header map into wire-contracts.md §2's flat
// string map: keys lower-cased ("SHOULD be written lower-case"), last value wins on
// duplicates.
func headersFrom(h http.Header) map[string]string {
	flat := make(map[string]string, len(h))
	for key, values := range h {
		if len(values) == 0 {
			continue
		}
		flat[strings.ToLower(key)] = values[len(values)-1]
	}
	return flat
}

func writeNativeResponse(w http.ResponseWriter, resp wire.Response) {
	for key, value := range resp.Headers {
		w.Header().Set(key, value)
	}
	w.WriteHeader(httpstatus.ToHTTP(benzene.Status(resp.StatusCode)))
	if resp.Body != "" {
		w.Write([]byte(resp.Body))
	}
}
