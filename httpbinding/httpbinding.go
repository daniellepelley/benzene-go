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
// calls for. A Path is matched segment-wise against r.URL.Path: a literal segment must match
// exactly, and a "{name}" segment captures that (non-empty) path segment as a route
// parameter, delivered to the pipeline as the wire header "route-<name>" (lower-cased). A
// route with no "{name}" segments is an exact string match, and exact routes always win over
// templated ones; templated routes are tried in registration order. There are no multi-
// segment wildcards - a parameter matches exactly one segment.
//
// The "route-" prefix is written after the inbound HTTP headers are flattened, so a client
// sending a literal "route-id" header can never spoof a path parameter.
type Route struct {
	Method string
	Path   string
	Topic  benzene.Topic
}

// templateRoute is a pre-split Route whose Path contains "{name}" segments.
type templateRoute struct {
	method   string
	segments []string
	topic    benzene.Topic
}

// Handler builds a native HTTP entry point: an incoming request is matched against routes
// (see Route for the matching rules), dispatched through builder's pipeline (via
// envelope.Dispatch), and the result mapped back to a real HTTP status code
// (httpstatus.ToHTTP) and JSON body.
//
// Scope: one DI scope per request (transport-bindings.md §1.6). Cancellation: the request's
// own context (its Done channel fires on client disconnect or server shutdown) - core-concepts
// §4's "no cancellation parameter on the pipeline signature, it rides on ctx" rule.
//
// Response headers: headers set during the invocation - by middleware on
// InvocationContext.ResponseHeaders, or by a handler via benzene.SetResponseHeader - come
// back on the wire.Response and are written as real HTTP response headers here.
func Handler(builder *benzene.ApplicationBuilder, routes []Route) http.Handler {
	table := NewRouteTable(routes)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		topic, params, ok := table.Match(r.Method, r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		headers := headersFrom(r.Header)
		for name, value := range params {
			headers["route-"+name] = value
		}

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, wire.Request{
			Topic:   topic.String(),
			Headers: headers,
			Body:    string(body),
		})
		writeNativeResponse(w, resp)
	})
}

// RouteTable is a compiled set of Routes. It exists as an exported type so every binding
// that routes (method, path) pairs shares one matching implementation - httpbinding itself,
// awslambda.HTTPHandler, and azurefunctions.Handler all accept []Route and must agree on
// what a Route means.
type RouteTable struct {
	exact     map[string]benzene.Topic
	templated []templateRoute
}

// NewRouteTable compiles routes: paths without "{" into the exact-match table, the rest into
// the templated list (in registration order).
func NewRouteTable(routes []Route) *RouteTable {
	table := &RouteTable{exact: make(map[string]benzene.Topic, len(routes))}
	for _, route := range routes {
		if strings.Contains(route.Path, "{") {
			table.templated = append(table.templated, templateRoute{
				method:   strings.ToUpper(route.Method),
				segments: strings.Split(route.Path, "/"),
				topic:    route.Topic,
			})
			continue
		}
		table.exact[routeKey(route.Method, route.Path)] = route.Topic
	}
	return table
}

// Match resolves (method, path) per Route's rules: the exact table first, then the templated
// routes in registration order. params holds any captured "{name}" segments (nil when the
// match was exact or captured nothing) - the caller writes them as "route-<name>" wire
// headers after flattening the inbound transport headers, so they can't be spoofed.
func (t *RouteTable) Match(method, path string) (benzene.Topic, map[string]string, bool) {
	if topic, ok := t.exact[routeKey(method, path)]; ok {
		return topic, nil, true
	}
	for _, route := range t.templated {
		if params, ok := matchTemplate(route, method, path); ok {
			return route.topic, params, true
		}
	}
	return benzene.Topic{}, nil, false
}

// matchTemplate matches path against one templated route. A "{name}" segment captures its
// (non-empty) path segment; a malformed or empty template segment ("{}", "{x") is treated as
// a literal. Parameter names are lower-cased for the "route-<name>" header.
func matchTemplate(route templateRoute, method, path string) (map[string]string, bool) {
	if route.method != strings.ToUpper(method) {
		return nil, false
	}
	segments := strings.Split(path, "/")
	if len(segments) != len(route.segments) {
		return nil, false
	}
	var params map[string]string
	for i, pattern := range route.segments {
		if len(pattern) > 2 && strings.HasPrefix(pattern, "{") && strings.HasSuffix(pattern, "}") {
			if segments[i] == "" {
				return nil, false
			}
			if params == nil {
				params = map[string]string{}
			}
			params[strings.ToLower(pattern[1:len(pattern)-1])] = segments[i]
			continue
		}
		if pattern != segments[i] {
			return nil, false
		}
	}
	return params, true
}

// Well-known paths from the default service standard (the main repo's
// docs/specification/design-principles.md §5): framework-provided HTTP surfaces mount under
// the /benzene/ prefix, so a URL, a log line, or a gateway rule can tell framework
// infrastructure from domain endpoints - and a single path rule can expose or protect all of
// it at once. Like every Benzene steer, these are defaults, not requirements: mount the
// handlers anywhere you like.
const (
	// EnvelopePath is the standard mount for EnvelopeHandler - the service-to-service
	// (and mesh collector) wire-envelope surface.
	EnvelopePath = "/benzene/invoke"
	// HealthPath is the standard mount for a Route serving the reserved healthcheck topic.
	HealthPath = "/benzene/health"
)

// EnvelopeHandler builds an HTTP entry point that speaks the wire-contracts.md §1 envelope
// directly: the request body is a wire.Request (any method/path - the caller POSTs the
// envelope), and the response body is a wire.Response. The outer HTTP status is always 200;
// the real Benzene outcome travels inside the envelope's own statusCode field, matching how a
// Lambda invoke or an internal function call carries the envelope with no HTTP layer at all.
// The default service standard mounts it at EnvelopePath.
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
