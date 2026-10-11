// Package httpbinding is the HTTP transport binding described by
// daniellepelley/Benzene's docs/specification/transport-bindings.md §2 ("HTTP (ASP.NET Core)"
// entry, ported to Go's net/http): topic resolved from route/method conventions, headers both
// directions, status via httpstatus's wire-contracts.md §4.1 table, one DI scope per request,
// cancellation from the request's context.
//
// It offers two entry points:
//   - Handler: a native REST-style handler - real HTTP status codes, an explicit route table.
//   - EnvelopeHandler: the wire-contracts.md §1 envelope carried directly over HTTP (HTTP 200
//     for every envelope it processed; the outcome travels in the envelope's own statusCode and
//     isSuccessful) - the envelope endpoint of transport-bindings.md §1, useful for
//     service-to-service calls with no route table to agree on.
package httpbinding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
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
//
// A "{version}" segment is additionally special-cased as the HTTP version carrier
// (versioning.md §2.1, e.g. Path: "/v{version}/orders/{id}"): sugar for the benzene-version
// header, its captured value sets the dispatched topic's version exactly as if the header had
// been sent, taking priority over a benzene-version header on the same request. A route with no
// "{version}" segment reads the header instead, so one topic can have a path-versioned and a
// header-versioned surface without duplicating routes.
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
//
// A bodiless request on a Route mapped to the reserved benzene:spec topic dispatches its "type"
// and "format" query parameters as the {type?, format?} request, as SpecHandler does (RouteBody).
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
		if version, ok := params["version"]; ok {
			topic = topic.WithVersion(version)
		}

		resp, _ := envelope.DispatchTopicResult(r.Context(), builder.Pipeline, builder.Container, topic, headers, RouteBody(topic, string(body), r.URL.Query()))
		writeNativeResponse(w, topic, resp)
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

// matchTemplate matches path against one templated route. A "{name}" placeholder captures its
// (non-empty) path segment; a literal prefix and/or suffix may surround the one placeholder in
// the same segment (e.g. "v{version}" - versioning.md §2.1's HTTP route-parameter convention),
// each still required to match exactly. A malformed or empty placeholder ("{}", "{x") is treated
// as a literal segment. Parameter names are lower-cased for the "route-<name>" header.
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
		name, prefix, suffix, isParam := parseSegmentPlaceholder(pattern)
		if !isParam {
			if pattern != segments[i] {
				return nil, false
			}
			continue
		}
		value := segments[i]
		if len(value) < len(prefix)+len(suffix) || !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
			return nil, false
		}
		captured := value[len(prefix) : len(value)-len(suffix)]
		if captured == "" {
			return nil, false
		}
		if params == nil {
			params = map[string]string{}
		}
		params[strings.ToLower(name)] = captured
	}
	return params, true
}

// parseSegmentPlaceholder reports whether pattern contains exactly one "{name}" placeholder and,
// if so, splits it into the literal text before and after the placeholder and the parameter name
// - e.g. "v{version}" -> prefix "v", name "version", suffix "". A bare "{name}" (the common case)
// yields empty prefix and suffix. A pattern with no placeholder, an empty one ("{}"), an unclosed
// one ("{x"), or more than one ("{a}{b}") is not a param at all - matchTemplate then matches the
// whole pattern as a literal segment, preserving the pre-existing malformed-placeholder handling.
func parseSegmentPlaceholder(pattern string) (name, prefix, suffix string, isParam bool) {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return "", "", "", false
	}
	close := strings.IndexByte(pattern[open+1:], '}')
	if close < 0 {
		return "", "", "", false
	}
	close += open + 1
	if close == open+1 { // "{}" - an empty placeholder name
		return "", "", "", false
	}
	rest := pattern[close+1:]
	if strings.ContainsAny(rest, "{}") { // a second placeholder - not the single-placeholder shape
		return "", "", "", false
	}
	return pattern[open+1 : close], pattern[:open], rest, true
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
	// SpecPath is the standard mount for SpecHandler, which serves the Contract Document (the
	// Cloud Service Profile's R5) on the reserved benzene:spec topic.
	SpecPath = "/benzene/spec"
)

// SpecHandler serves the reserved benzene:spec topic over HTTP (contract-document.md §7): a GET
// copies its "type" and "format" query parameters onto a {type?, format?} request and dispatches it
// through builder's pipeline - so GET /benzene/spec runs the same middleware, authentication
// included, as every other topic, and the pipeline's contract.Middleware answers it. The result
// maps to HTTP like any route's: 200 with the document, 501 for a type or format the service does
// not serve. The default service standard mounts it at SpecPath. Methods other than GET and HEAD
// are answered 405.
func SpecHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		topic := benzene.NewTopic(contract.TopicID)
		resp, _ := envelope.DispatchTopicResult(r.Context(), builder.Pipeline, builder.Container, topic, headersFrom(r.Header), SpecRequestBody(r.URL.Query()))
		writeNativeResponse(w, topic, resp)
	})
}

// SpecRequestBody is the benzene:spec request an HTTP query carries (contract-document.md §7): a
// {type?, format?} object holding the "type" and "format" parameters that are present. SpecHandler
// dispatches it; Handler, awslambda.HTTPHandler and azurefunctions.Handler dispatch it for a
// bodiless request on a Route mapped to benzene:spec, so ?type= and ?format= reach
// contract.Middleware on every HTTP front door.
func SpecRequestBody(query url.Values) string {
	request := map[string]string{}
	for _, name := range []string{"type", "format"} {
		if query.Has(name) {
			request[name] = query.Get(name)
		}
	}
	body, _ := json.Marshal(request) // a map of strings always marshals
	return string(body)
}

// RouteBody is the body a Route-based HTTP handler dispatches for topic: the request body as sent,
// except that a bodiless request on a Route mapped to benzene:spec carries its query as the
// {type?, format?} request (SpecRequestBody). awslambda.HTTPHandler and azurefunctions.Handler
// share it with Handler.
func RouteBody(topic benzene.Topic, body string, query url.Values) string {
	if topic.ID == contract.TopicID && strings.TrimSpace(body) == "" {
		return SpecRequestBody(query)
	}
	return body
}

// EnvelopeOption configures EnvelopeHandler.
type EnvelopeOption func(*envelopeConfig)

type envelopeConfig struct {
	allow func(topic string) bool
}

// WithTopicFilter restricts which topics the envelope endpoint reaches (transport-bindings.md §1):
// a request whose topic allow rejects is answered not-found, exactly as if no handler matched, and
// never reaches the pipeline. The endpoint otherwise reaches every handler the service registers,
// including topics with no HTTP route of their own, so a service exposing it beyond its own trust
// boundary should restrict it. allow sees the envelope's topic id; reserved topics (benzene:*) are
// filtered like any other, so allow the ones the endpoint should still answer:
//
//	httpbinding.EnvelopeHandler(builder, httpbinding.WithTopicFilter(func(topic string) bool {
//		return strings.HasPrefix(topic, "orders:") || topic == healthcheck.ReservedTopic
//	}))
func WithTopicFilter(allow func(topic string) bool) EnvelopeOption {
	return func(c *envelopeConfig) { c.allow = allow }
}

// EnvelopeHandler builds the envelope endpoint (transport-bindings.md §1): an HTTP entry point that
// speaks the wire-contracts.md §1 envelope directly. The request body is a wire.Request (any
// method/path - the caller POSTs the envelope), and the response body is the wire.Response. The
// default service standard mounts it at EnvelopePath.
//
// It answers HTTP 200 for every envelope it processed, whatever the result - not-found, a
// validation-error, an unauthorized from the pipeline's own auth middleware and a deleted alike -
// because the envelope carries the outcome (statusCode, isSuccessful) and a client reads it, never
// the status line; a problem document inside it carries no `status` member. A non-2xx status means
// the carrier failed and no envelope was processed: 400 for a body that is not an envelope, 500 if
// the response cannot be written. Authentication the HTTP host enforces before the envelope is read
// (401, 403) belongs in an ordinary net/http middleware wrapping this handler; authentication by
// topic belongs in the pipeline, which this endpoint runs exactly like every other binding.
//
// Use WithTopicFilter to restrict the topics it reaches.
func EnvelopeHandler(builder *benzene.ApplicationBuilder, opts ...EnvelopeOption) http.Handler {
	var cfg envelopeConfig
	for _, opt := range opts {
		opt(&cfg)
	}

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

		pipeline := builder.Pipeline
		if cfg.allow != nil && !cfg.allow(req.Topic) {
			pipeline = restricted
		}
		resp := envelope.Dispatch(r.Context(), pipeline, builder.Container, req)
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

// restricted is the pipeline a topic outside WithTopicFilter runs: the router's own not-found, so
// a restricted topic cannot be told apart from one with no handler.
var restricted = benzene.NewPipeline(func(_ context.Context, ic *benzene.InvocationContext, _ func(context.Context) error) error {
	ic.Result = benzene.NotFound[any](fmt.Sprintf("no handler found for topic %q", ic.Topic.ID))
	return nil
})

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

func writeNativeResponse(w http.ResponseWriter, topic benzene.Topic, resp wire.Response) {
	// httpstatus.Response, not a rendering of our own: the status code, the problem document's
	// `status` member and the problem content type are wire-contracts.md §4.1's business, shared
	// with every other HTTP binding in this port so none of them can drift.
	code, body, headers := httpstatus.Response(topic, resp)
	for key, value := range headers {
		w.Header().Set(key, value)
	}

	w.WriteHeader(code)
	if body != "" {
		w.Write([]byte(body))
	}
}

// The listen-address convention for an HTTP-fronted Benzene service. Cloud Run, Knative, App
// Runner, Heroku and most container PaaS name the port to bind in the PORT environment
// variable; a service that binds anything else is unreachable, so the convention belongs here
// rather than being restated in every main().
const (
	// PortEnvVar is the environment variable the platform names the listen port in.
	PortEnvVar = "PORT"
	// DefaultPort is the port ListenAddr falls back to when PortEnvVar is unset - local runs
	// and any host that does not name one.
	DefaultPort = "8080"
)

// ListenAddr returns the address an HTTP-fronted service should pass to http.ListenAndServe or
// http.Server.Addr: ":" + $PORT, falling back to ":" + DefaultPort when PORT is unset or empty.
//
// It is a one-line shorthand composed from public API and the standard library, and the
// explicit form it composes is exactly:
//
//	port := os.Getenv(httpbinding.PortEnvVar)
//	if port == "" {
//		port = httpbinding.DefaultPort
//	}
//	addr := ":" + port
//
// Drop to that form to bind a specific interface, to read the port from a flag or config file
// instead, or to fail loudly when the platform named no port.
func ListenAddr() string {
	port := os.Getenv(PortEnvVar)
	if port == "" {
		port = DefaultPort
	}
	return ":" + port
}
