// Package openapi derives an OpenAPI 3.0 document from a Benzene service's Contract Document - the
// Go form of Benzene.Schema.OpenApi. It is zero-dependency and adds no reflection: the contract
// package derives the document once at start-up, with its schemas already in the OpenAPI 3.0
// dialect and its named types in components.schemas, and this package only reshapes it into the
// OpenAPI wire format - the schemas and their #/components/schemas/ references carry over as they
// are.
//
// The Cloud Service Profile's R5 (a derived spec document) is satisfied by the Contract Document
// itself, served on benzene:spec (GET /benzene/spec). This is an industry-standard view for readers
// who want OpenAPI: every request the service serves becomes a POST operation whose request body is
// the topic's request schema and whose responses carry the response schema (success) and the
// Benzene failure vocabulary mapped to HTTP status codes (httpstatus). It is a documentation view
// of the service's message contracts, not a claim that every topic is reachable over HTTP - a
// queue-shaped topic is documented the same way, since the value is the request/response shape, not
// the transport.
//
// AsyncAPI generation (the other half of Benzene.Schema.OpenApi) ships in the sibling asyncapi
// package, which reads the same document: every request is a "receive" operation, and the
// service's declared events and sends are "send" operations.
package openapi

import (
	"sort"
	"strconv"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/httpstatus"
)

// Document is a minimal OpenAPI 3.0 document: enough of the shape to describe a Benzene service's
// requests as operations. Schema objects are held as map[string]any, copied from the Contract
// Document, whose named schemas become Components.Schemas under the same names.
type Document struct {
	OpenAPI    string              `json:"openapi"`
	Info       Info                `json:"info"`
	Paths      map[string]PathItem `json:"paths"`
	Components *Components         `json:"components,omitempty"`
}

// Components holds the named schemas the operations' $refs point into (#/components/schemas/...).
type Components struct {
	Schemas map[string]map[string]any `json:"schemas,omitempty"`
}

// Info is the OpenAPI info object: the service's identity.
type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// PathItem is one path's operations. Benzene invocations are message dispatches, modeled as POST.
type PathItem struct {
	Post *Operation `json:"post,omitempty"`
}

// Operation is one topic's operation: request body in, the response/error vocabulary out.
type Operation struct {
	OperationID string              `json:"operationId"`
	Summary     string              `json:"summary,omitempty"`
	Description string              `json:"description,omitempty"`
	RequestBody *RequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]Response `json:"responses"`
}

// RequestBody is an operation's request body (application/json).
type RequestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]MediaType `json:"content"`
}

// Response is one HTTP status code's response.
type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// MediaType carries a schema for a content type.
type MediaType struct {
	Schema map[string]any `json:"schema,omitempty"`
}

type config struct {
	title       string
	version     string
	description string
	pathPrefix  string
}

// Option configures Generate.
type Option func(*config)

// WithTitle overrides the document title (default: the document's service name, or
// "benzene-service" when that is empty).
func WithTitle(title string) Option { return func(c *config) { c.title = title } }

// WithVersion overrides the document version (default: the document's service version, or
// "0.0.0" when that is empty).
func WithVersion(version string) Option { return func(c *config) { c.version = version } }

// WithDescription overrides the info description (default: the document's description).
func WithDescription(description string) Option {
	return func(c *config) { c.description = description }
}

// WithPathPrefix sets the prefix each topic's operation path is built under (default "/"). A topic
// "order:create" under the default prefix becomes the path "/order:create".
func WithPathPrefix(prefix string) Option { return func(c *config) { c.pathPrefix = prefix } }

// Generate builds an OpenAPI 3.0 document from doc (typically contract.Derive's result, or
// cloudservice.Service's Contract). Each request becomes a POST operation carrying its request
// schema and response/error vocabulary; doc's components become the OpenAPI components. The schemas
// are deep-copied, so Generate never shares or mutates the document's maps. The result marshals to
// valid OpenAPI 3.0 JSON with encoding/json; map keys marshal sorted, so the output is deterministic
// for a given document.
func Generate(doc contract.Document, opts ...Option) *Document {
	cfg := config{title: doc.Service, version: doc.ServiceVersion, description: doc.Description, pathPrefix: "/"}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.title == "" {
		cfg.title = "benzene-service"
	}
	if cfg.version == "" {
		cfg.version = "0.0.0"
	}

	out := &Document{
		OpenAPI: "3.0.3",
		Info:    Info{Title: cfg.title, Version: cfg.version, Description: cfg.description},
		Paths:   make(map[string]PathItem, len(doc.Requests)),
	}
	if len(doc.Components.Schemas) > 0 {
		out.Components = &Components{Schemas: make(map[string]map[string]any, len(doc.Components.Schemas))}
		for name, schema := range doc.Components.Schemas {
			out.Components.Schemas[name] = cloneSchema(schema)
		}
	}
	// operationId MUST be unique across the whole document (OpenAPI 3.0). Distinct topic ids can
	// sanitize to the same id ("user:get" and "user-get" both -> "user_get") while still producing
	// distinct path keys, so track assigned ids and disambiguate with a suffix in document order.
	usedIDs := make(map[string]bool, len(doc.Requests))
	for _, request := range doc.Requests {
		if contract.IsReserved(request.Topic) {
			continue // a consumer ignores a reserved entry (contract-document.md §1)
		}
		op := &Operation{
			OperationID: uniqueOperationID(request.Topic, usedIDs),
			Summary:     "Handle " + request.Topic,
			Responses:   responsesFor(request),
		}
		if request.Version != "" {
			op.Description = "Topic version " + request.Version
		}
		if request.Request != nil {
			op.RequestBody = &RequestBody{Required: true, Content: jsonContent(request.Request)}
		}
		out.Paths[pathFor(cfg.pathPrefix, request.Topic)] = PathItem{Post: op}
	}
	return out
}

// documentedFailures is the framework failure vocabulary an operation may return, documented as
// error responses grouped by their HTTP status code (httpstatus). Success is documented as a single
// 200 carrying the response schema - a handler may return any success status (Created/Accepted/...),
// but 200 is the canonical documentation code and the response body shape is the same either way.
var documentedFailures = []benzene.Status{
	benzene.StatusBadRequest,
	benzene.StatusValidationError,
	benzene.StatusUnauthorized,
	benzene.StatusForbidden,
	benzene.StatusNotFound,
	benzene.StatusConflict,
	benzene.StatusTooManyRequests,
	benzene.StatusTimeout,
	benzene.StatusNotImplemented,
	benzene.StatusServiceUnavailable,
	benzene.StatusUnexpectedError,
}

// errorSchema is the Benzene failure body: a status code plus zero or more error messages
// (Result.Status + Result.Errors, wire-contracts.md §1).
func errorSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"statusCode": map[string]any{"type": "string"},
			"errors":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

// responsesFor builds a topic's responses: 200 (success, the response schema) plus one response per
// distinct HTTP status code the failure vocabulary maps to, each listing the Benzene statuses that
// share it so a reader can recover the precise status from the error body's statusCode.
func responsesFor(request contract.Request) map[string]Response {
	responses := map[string]Response{
		strconv.Itoa(httpstatus.ToHTTP(benzene.StatusOk)): {
			Description: "Successful invocation.",
			Content:     jsonContent(request.Response),
		},
	}

	// Group failure statuses by their HTTP code, preserving the documentedFailures order within each
	// group so the description reads deterministically.
	byCode := map[int][]string{}
	var codeOrder []int
	for _, status := range documentedFailures {
		code := httpstatus.ToHTTP(status)
		if _, seen := byCode[code]; !seen {
			codeOrder = append(codeOrder, code)
		}
		byCode[code] = append(byCode[code], string(status))
	}
	sort.Ints(codeOrder)
	for _, code := range codeOrder {
		key := strconv.Itoa(code)
		if _, taken := responses[key]; taken {
			continue // don't clobber the success code if a failure ever mapped to it
		}
		responses[key] = Response{
			Description: "Failure: " + strings.Join(byCode[code], ", ") + ".",
			Content:     map[string]MediaType{"application/json": {Schema: errorSchema()}},
		}
	}
	return responses
}

// jsonContent is an application/json body carrying a copy of schema, or no content when the entry
// declares none (an absent response: the handler returns no payload).
func jsonContent(schema contract.Schema) map[string]MediaType {
	if schema == nil {
		return nil
	}
	return map[string]MediaType{"application/json": {Schema: cloneSchema(schema)}}
}

// cloneSchema deep-copies a schema's maps and slices.
func cloneSchema(schema map[string]any) map[string]any {
	return cloneValue(schema).(map[string]any)
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = cloneValue(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = cloneValue(child)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	}
	return v
}

// uniqueOperationID returns operationID(topicID), guaranteed non-empty and unique among the ids
// already in used: an empty base (a topic id with no alphanumerics, e.g. ":") falls back to
// "operation", and a collision gets a "_2", "_3", ... suffix. This keeps the document valid per
// OpenAPI 3.0's operationId-uniqueness rule regardless of topic naming. Disambiguation follows the
// document's request order, so it is stable for a given document.
func uniqueOperationID(topicID string, used map[string]bool) string {
	base := operationID(topicID)
	if base == "" {
		base = "operation"
	}
	id := base
	for n := 2; used[id]; n++ {
		id = base + "_" + strconv.Itoa(n)
	}
	used[id] = true
	return id
}

// operationID turns a topic id into a valid operationId: non-alphanumeric runs become a single
// underscore (so "order:create" -> "order_create"), matching how an OpenAPI operationId is
// conventionally a bare identifier. It may return "" for a topic id with no alphanumerics; callers
// go through uniqueOperationID, which supplies a non-empty fallback.
func operationID(topicID string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range topicID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// pathFor joins the configured prefix and the topic id into an OpenAPI path key, collapsing a
// double slash so a prefix of "/" or "/api/" both yield a single separator. The topic id is used
// verbatim (a colon is a valid path character, so "order:create" -> "/order:create"); a topic id
// containing "{" or "}" would read as OpenAPI path templating, but Benzene topic ids are
// conventionally "name:action" and do not, so no escaping is applied.
func pathFor(prefix, topicID string) string {
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix + topicID
}
