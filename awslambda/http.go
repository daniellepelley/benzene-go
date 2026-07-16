package awslambda

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpstatus"
	"github.com/daniellepelley/benzene-go/wire"
)

// httpEvent mirrors the fields this adapter needs from the two HTTP event shapes a Lambda can
// receive, distinguished by which fields are populated:
//
//   - API Gateway HTTP API v2.0 / Lambda Function URL - method and path nested under
//     requestContext.http; see
//     https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html#urls-payloads. A
//     Function URL needs no API Gateway resource at all, making it the simplest way to expose
//     a Lambda over HTTP.
//   - API Gateway REST API / HTTP API v1.0 payload / Application Load Balancer target group -
//     top-level httpMethod and path; ALB adds multiValueHeaders when the target group has
//     multi-value headers enabled (in that mode the flat headers map is absent). See
//     https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html
//     and https://docs.aws.amazon.com/elasticloadbalancing/latest/application/lambda-functions.html.
//
// Both are transport-bindings.md's "API Gateway (HTTP-like: topic from route, headers from
// HTTP headers)" AWS Lambda catalog entry, applied to any of the three front doors.
type httpEvent struct {
	// v2.0 / Function URL fields.
	RawPath        string `json:"rawPath"`
	RequestContext struct {
		HTTP struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"http"`
	} `json:"requestContext"`

	// v1.0 / ALB fields.
	HTTPMethod        string              `json:"httpMethod"`
	Path              string              `json:"path"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders"`

	// Shared fields.
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
}

// isV1 reports whether the event is the REST/v1.0/ALB shape. Only that shape has a top-level
// httpMethod; v2.0 nests the method under requestContext.http.
func (e *httpEvent) isV1() bool {
	return e.HTTPMethod != ""
}

// usedMultiValueHeaders reports whether the caller is an ALB target group with multi-value
// headers enabled - the one mode whose response must carry multiValueHeaders (the flat
// headers map is ignored there, in both directions).
func (e *httpEvent) usedMultiValueHeaders() bool {
	return len(e.Headers) == 0 && e.MultiValueHeaders != nil
}

type httpV2Response struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
}

// httpV1Response is the REST/v1.0/ALB response shape. isBase64Encoded is emitted explicitly
// (not omitempty) because ALB requires the field; multiValueHeaders is populated only for the
// multi-value ALB mode (see httpEvent.usedMultiValueHeaders).
type httpV1Response struct {
	StatusCode        int                 `json:"statusCode"`
	Headers           map[string]string   `json:"headers,omitempty"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders,omitempty"`
	Body              string              `json:"body"`
	IsBase64Encoded   bool                `json:"isBase64Encoded"`
}

// HTTPHandler adapts routes (matched exactly like httpbinding.Route) into a HandlerFunc for a
// Lambda fronted by any of AWS's HTTP front doors - a Function URL or API Gateway HTTP API
// (v2.0 payload), an API Gateway REST API or HTTP API v1.0 payload, or an Application Load
// Balancer target group. The event shape is detected per invocation (see httpEvent), the
// response uses the matching shape, and either way the invocation carries real HTTP status
// codes via httpstatus.ToHTTP, dispatched through envelope.Dispatch exactly like
// httpbinding.Handler.
func HTTPHandler(builder *benzene.ApplicationBuilder, routes []httpbinding.Route) HandlerFunc {
	byKey := make(map[string]benzene.Topic, len(routes))
	for _, route := range routes {
		byKey[routeKey(route.Method, route.Path)] = route.Topic
	}

	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var req httpEvent
		if err := json.Unmarshal(event, &req); err != nil {
			return nil, fmt.Errorf("awslambda: malformed HTTP event: %w", err)
		}

		method, path := req.RequestContext.HTTP.Method, req.RequestContext.HTTP.Path
		if req.isV1() {
			method, path = req.HTTPMethod, req.Path
		} else if path == "" {
			path = req.RawPath
		}

		topic, ok := byKey[routeKey(method, path)]
		if !ok {
			return marshalResponse(&req, http.StatusNotFound, nil, "not found")
		}

		body := req.Body
		if req.IsBase64Encoded {
			decoded, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				return marshalResponse(&req, http.StatusBadRequest, nil, "malformed base64 body")
			}
			body = string(decoded)
		}

		resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, wire.Request{
			Topic:   topic.String(),
			Headers: requestHeaders(&req),
			Body:    body,
		})

		return marshalResponse(&req, httpstatus.ToHTTP(benzene.Status(resp.StatusCode)), resp.Headers, resp.Body)
	}
}

// requestHeaders produces the wire-contracts.md §2 flat header map for either event shape:
// keys lower-cased, last value wins on duplicates (matching httpbinding's headersFrom). v2.0
// events arrive already flattened and lower-cased, so they pass through; v1.0/ALB events are
// normalized here, falling back to multiValueHeaders for the multi-value ALB mode.
func requestHeaders(req *httpEvent) map[string]string {
	if !req.isV1() {
		return req.Headers
	}
	if req.usedMultiValueHeaders() {
		flat := make(map[string]string, len(req.MultiValueHeaders))
		for key, values := range req.MultiValueHeaders {
			if len(values) == 0 {
				continue
			}
			flat[strings.ToLower(key)] = values[len(values)-1]
		}
		return flat
	}
	flat := make(map[string]string, len(req.Headers))
	for key, value := range req.Headers {
		flat[strings.ToLower(key)] = value
	}
	return flat
}

// marshalResponse writes the response in the shape the inbound event promised: v2.0 events
// get the v2.0 shape, v1.0/ALB events get the v1.0 shape, and the multi-value ALB mode
// additionally gets each header echoed under multiValueHeaders (the only header field that
// mode honors).
func marshalResponse(req *httpEvent, status int, headers map[string]string, body string) (json.RawMessage, error) {
	if !req.isV1() {
		return json.Marshal(httpV2Response{StatusCode: status, Headers: headers, Body: body})
	}
	resp := httpV1Response{StatusCode: status, Headers: headers, Body: body}
	if req.usedMultiValueHeaders() && len(headers) > 0 {
		resp.MultiValueHeaders = make(map[string][]string, len(headers))
		for key, value := range headers {
			resp.MultiValueHeaders[key] = []string{value}
		}
	}
	return json.Marshal(resp)
}

func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}
