// Package openapi derives an OpenAPI 3.0 document from a Benzene service's registered topics -
// the Go form of Benzene.Schema.OpenApi. It is zero-dependency: it reuses the request/response
// JSON Schemas the mesh package already derives from each handler's TReq/TRes types at startup
// (the one sanctioned use of reflection, never on the dispatch path), so this package adds no
// reflection of its own - it only reshapes the derived descriptor into the OpenAPI wire format.
//
// The Cloud Service Profile's R5 (a derived spec document) is already satisfied by
// mesh.SpecHandler serving Benzene's own descriptor format, which the profile permits. This is
// the richer, industry-standard alternative for readers who want OpenAPI: every registered topic
// becomes a POST operation whose request body is the topic's request schema and whose responses
// carry the response schema (success) and the Benzene failure vocabulary mapped to HTTP status
// codes (httpstatus). It is a documentation view of the service's message contracts, not a claim
// that every topic is reachable over HTTP - a queue-shaped topic is documented the same way, since
// the value is the request/response shape, not the transport.
//
// AsyncAPI generation for event topics (the other half of Benzene.Schema.OpenApi) is a separate
// follow-up: the descriptor does not classify a topic as request/response vs fire-and-forget, so a
// faithful AsyncAPI document needs an input this package deliberately does not fabricate yet.
package openapi

import (
	"sort"
	"strconv"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpstatus"
	"github.com/daniellepelley/benzene-go/mesh"
)

// Document is a minimal OpenAPI 3.0 document: enough of the shape to describe a Benzene service's
// topics as operations. Schema objects are held as map[string]any (the derived JSON Schema form,
// converted to OpenAPI 3.0's nullable convention), since the derived schemas are anonymous - the
// descriptor carries no Go type name to hoist them into components/schemas under a $ref.
type Document struct {
	OpenAPI string              `json:"openapi"`
	Info    Info                `json:"info"`
	Paths   map[string]PathItem `json:"paths"`
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

// WithTitle overrides the document title (default: the descriptor's service name, or
// "benzene-service" when that is empty).
func WithTitle(title string) Option { return func(c *config) { c.title = title } }

// WithVersion overrides the document version (default: the descriptor's service version, or
// "0.0.0" when that is empty).
func WithVersion(version string) Option { return func(c *config) { c.version = version } }

// WithDescription sets the info description.
func WithDescription(description string) Option {
	return func(c *config) { c.description = description }
}

// WithPathPrefix sets the prefix each topic's operation path is built under (default "/"). A topic
// "order:create" under the default prefix becomes the path "/order:create".
func WithPathPrefix(prefix string) Option { return func(c *config) { c.pathPrefix = prefix } }

// Generate builds an OpenAPI 3.0 document from desc (typically mesh.Describe(registry, info)). Each
// topic becomes a POST operation carrying its request schema and response/error vocabulary. The
// result marshals to valid OpenAPI 3.0 JSON with encoding/json; map keys marshal sorted, so the
// output is deterministic for a given descriptor.
func Generate(desc mesh.Descriptor, opts ...Option) *Document {
	cfg := config{title: desc.Service, version: desc.ServiceVersion, pathPrefix: "/"}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.title == "" {
		cfg.title = "benzene-service"
	}
	if cfg.version == "" {
		cfg.version = "0.0.0"
	}

	doc := &Document{
		OpenAPI: "3.0.3",
		Info:    Info{Title: cfg.title, Version: cfg.version, Description: cfg.description},
		Paths:   make(map[string]PathItem, len(desc.Topics)),
	}
	// operationId MUST be unique across the whole document (OpenAPI 3.0). Distinct topic ids can
	// sanitize to the same id ("user:get" and "user-get" both -> "user_get") while still producing
	// distinct path keys, so track assigned ids and disambiguate with a suffix in topic order.
	usedIDs := make(map[string]bool, len(desc.Topics))
	for _, topic := range desc.Topics {
		op := &Operation{
			OperationID: uniqueOperationID(topic.ID, usedIDs),
			Summary:     "Handle " + topic.ID,
			Responses:   responsesFor(topic),
		}
		if topic.Version != "" {
			op.Description = "Topic version " + topic.Version
		}
		if topic.RequestSchema != nil {
			op.RequestBody = &RequestBody{
				Required: true,
				Content:  map[string]MediaType{"application/json": {Schema: toOpenAPISchema(topic.RequestSchema)}},
			}
		}
		doc.Paths[pathFor(cfg.pathPrefix, topic.ID)] = PathItem{Post: op}
	}
	return doc
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
func responsesFor(topic mesh.TopicDescriptor) map[string]Response {
	responses := map[string]Response{
		strconv.Itoa(httpstatus.ToHTTP(benzene.StatusOk)): {
			Description: "Successful invocation.",
			Content:     successContent(topic),
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

func successContent(topic mesh.TopicDescriptor) map[string]MediaType {
	if topic.ResponseSchema == nil {
		return nil
	}
	return map[string]MediaType{"application/json": {Schema: toOpenAPISchema(topic.ResponseSchema)}}
}

// toOpenAPISchema deep-copies a derived JSON Schema into an OpenAPI 3.0 Schema Object. The derived
// schemas use only keys OpenAPI shares (type/properties/required/items/additionalProperties/format),
// with one exception: mesh derives a nullable value as JSON Schema's type array (e.g.
// ["string","null"]), which OpenAPI 3.0 does not allow - it uses "nullable": true instead. This
// converts that form and recurses into nested schemas. The copy leaves the descriptor's maps
// untouched (Generate must be safe to call repeatedly on the same descriptor).
func toOpenAPISchema(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		switch key {
		case "type":
			if types, nullable := splitNullableType(value); nullable {
				out["type"] = types
				out["nullable"] = true
				continue
			}
			out["type"] = value
		case "properties":
			if props, ok := value.(map[string]any); ok {
				converted := make(map[string]any, len(props))
				for name, propSchema := range props {
					converted[name] = convertNested(propSchema)
				}
				out["properties"] = converted
				continue
			}
			out[key] = value
		case "items", "additionalProperties":
			out[key] = convertNested(value)
		default:
			out[key] = value
		}
	}
	return out
}

// convertNested applies toOpenAPISchema to a value that is itself a schema, passing non-schema
// values (e.g. additionalProperties: true, which mesh never emits but OpenAPI permits) through.
func convertNested(value any) any {
	if nested, ok := value.(map[string]any); ok {
		return toOpenAPISchema(nested)
	}
	return value
}

// splitNullableType detects mesh's nullable type array (["T","null"]) and returns the non-null type
// plus true; for a plain string type or any other shape it returns the value unchanged and false.
// It handles both the []string form (a descriptor straight from mesh.Describe) and the []any form (a
// descriptor that round-tripped through JSON), since either can reach Generate.
func splitNullableType(value any) (any, bool) {
	var elems []string
	switch typed := value.(type) {
	case []string:
		elems = typed
	case []any:
		for _, e := range typed {
			s, ok := e.(string)
			if !ok {
				return value, false
			}
			elems = append(elems, s)
		}
	default:
		return value, false
	}
	if len(elems) != 2 {
		return value, false
	}
	for i, e := range elems {
		if e == "null" {
			return elems[1-i], true // return the other (non-null) type
		}
	}
	return value, false
}

// uniqueOperationID returns operationID(topicID), guaranteed non-empty and unique among the ids
// already in used: an empty base (a topic id with no alphanumerics, e.g. ":") falls back to
// "operation", and a collision gets a "_2", "_3", ... suffix. This keeps the document valid per
// OpenAPI 3.0's operationId-uniqueness rule regardless of topic naming. Disambiguation follows the
// descriptor's topic order, so it is stable for a given descriptor.
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
