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

// httpV2Request mirrors the fields this adapter needs from the API Gateway HTTP API v2.0 /
// Lambda Function URL payload format - see
// https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html#urls-payloads. A Function
// URL needs no API Gateway resource at all, making it the simplest way to expose a Lambda over
// HTTP - transport-bindings.md's "API Gateway (HTTP-like: topic from route, headers from HTTP
// headers)" AWS Lambda catalog entry, applied to either front door.
type httpV2Request struct {
	RawPath        string            `json:"rawPath"`
	Headers        map[string]string `json:"headers"`
	RequestContext struct {
		HTTP struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"http"`
	} `json:"requestContext"`
	Body            string `json:"body"`
	IsBase64Encoded bool   `json:"isBase64Encoded"`
}

type httpV2Response struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
}

// HTTPHandler adapts routes (matched exactly like httpbinding.Route) into a HandlerFunc for a
// Lambda fronted by a Function URL or an API Gateway HTTP API (v2.0 payload format): real HTTP
// status codes via httpstatus.ToHTTP, dispatched through envelope.Dispatch exactly like
// httpbinding.Handler.
func HTTPHandler(builder *benzene.ApplicationBuilder, routes []httpbinding.Route) HandlerFunc {
	byKey := make(map[string]benzene.Topic, len(routes))
	for _, route := range routes {
		byKey[routeKey(route.Method, route.Path)] = route.Topic
	}

	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var req httpV2Request
		if err := json.Unmarshal(event, &req); err != nil {
			return nil, fmt.Errorf("awslambda: malformed HTTP v2 event: %w", err)
		}

		path := req.RequestContext.HTTP.Path
		if path == "" {
			path = req.RawPath
		}
		topic, ok := byKey[routeKey(req.RequestContext.HTTP.Method, path)]
		if !ok {
			return json.Marshal(httpV2Response{StatusCode: http.StatusNotFound, Body: "not found"})
		}

		body := req.Body
		if req.IsBase64Encoded {
			decoded, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				return json.Marshal(httpV2Response{StatusCode: http.StatusBadRequest, Body: "malformed base64 body"})
			}
			body = string(decoded)
		}

		resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, wire.Request{
			Topic:   topic.String(),
			Headers: req.Headers,
			Body:    body,
		})

		return json.Marshal(httpV2Response{
			StatusCode: httpstatus.ToHTTP(benzene.Status(resp.StatusCode)),
			Headers:    resp.Headers,
			Body:       resp.Body,
		})
	}
}

func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}
