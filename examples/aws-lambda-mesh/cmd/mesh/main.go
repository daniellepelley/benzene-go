// Command aws-lambda-mesh-mesh is the seventh Lambda of examples/aws-lambda-mesh: the mesh
// itself. Unlike .NET's/TypeScript's AwsMesh, this port's mesh does not discover the six service
// Lambdas by listing/tagging and interrogating them (no AWS Lambda ListFunctions/ListTags
// discovery provider, no S3-backed catalog store exist in this port) - it is a thin wrapper
// around meshd.Collector, reusing examples/k8s-mesh-helloworld's push-based collector pattern
// verbatim. See ../../README.md's "Divergence from .NET" section for the full story.
//
// One Lambda function answers two very different callers, distinguished the same way
// examples/aws-lambda-helloworld's newHandler tells an HTTP event from a direct invoke apart:
//
//   - a DIRECT LAMBDA INVOKE with no "requestContext" - the six service Lambdas pushing
//     register/heartbeat/traces/issues via awslambdaclient.Client (see ../../meshapp), dispatched
//     exactly like awslambda.EnvelopeHandler;
//   - an API-GATEWAY-FRONTED HTTP request - GET /benzene/fleet-ui (a human's browser, or curl)
//     serves the Mesh View; that page's own JS then polls the fleet by POSTing the wire envelope
//     to same-origin POST /benzene/invoke (httpbinding.EnvelopeHandler's contract, reimplemented
//     here for the Lambda HTTP event shape since awslambda has no generic net/http.Handler
//     bridge); GET /mesh/discovered is a tiny convenience wrapper, the same shape
//     examples/k8s-mesh-helloworld/cmd/mesh adds, so a human or CI can assert a service count
//     with one curl.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
	"github.com/daniellepelley/benzene-go/wire"
)

// discoveredResponse mirrors examples/k8s-mesh-helloworld/cmd/mesh's {"discovered":N} convenience
// shape, so both mesh examples answer the same way to the same kind of curl.
type discoveredResponse struct {
	Discovered int `json:"discovered"`
}

// httpEvent mirrors the fields this Lambda needs from an API Gateway HTTP API v2.0 / Function URL
// request - the same shape awslambda.HTTPHandler parses (unexported there, so restated here; this
// mesh Lambda's front door is deliberately small: two fixed routes plus the fleet UI, not a full
// route table).
type httpEvent struct {
	RawPath        string `json:"rawPath"`
	RequestContext struct {
		HTTP struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"http"`
	} `json:"requestContext"`
	Body            string `json:"body"`
	IsBase64Encoded bool   `json:"isBase64Encoded"`
}

type httpResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
}

func jsonResponse(status int, headers map[string]string, body string) (json.RawMessage, error) {
	return json.Marshal(httpResponse{StatusCode: status, Headers: headers, Body: body})
}

// newMeshHandler wraps collector as the composite Lambda entry point described in the package
// doc.
func newMeshHandler(collector *meshd.Collector) awslambda.HandlerFunc {
	builder := collector.Builder()
	envelopeHandler := awslambda.EnvelopeHandler(builder)
	viewHandler := collector.ViewHandler(httpbinding.EnvelopePath)

	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var probe struct {
			RequestContext json.RawMessage `json:"requestContext"`
		}
		if err := json.Unmarshal(event, &probe); err != nil || len(probe.RequestContext) == 0 {
			// No requestContext: a direct Lambda invoke carrying the raw wire envelope - a
			// service pushing register/heartbeat/traces/issues.
			return envelopeHandler(ctx, event)
		}

		var req httpEvent
		if err := json.Unmarshal(event, &req); err != nil {
			return nil, fmt.Errorf("mesh: malformed HTTP event: %w", err)
		}
		method, path := req.RequestContext.HTTP.Method, req.RequestContext.HTTP.Path
		if path == "" {
			path = req.RawPath
		}

		switch {
		case method == http.MethodGet && (path == meshd.ViewPath || path == "/"):
			rec := httptest.NewRecorder()
			viewHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			return jsonResponse(rec.Code, flattenHeaders(rec.Header()), rec.Body.String())

		case method == http.MethodGet && path == "/mesh/discovered":
			resp, _ := envelope.DispatchTopicResult(ctx, builder.Pipeline, builder.Container, benzene.NewTopic(mesh.TopicQueryFleet), nil, "")
			var fleet meshd.FleetView
			_ = json.Unmarshal([]byte(resp.Body), &fleet)
			data, err := json.Marshal(discoveredResponse{Discovered: len(fleet.Services)})
			if err != nil {
				return jsonResponse(http.StatusInternalServerError, nil, "failed to serialize discovered response")
			}
			return jsonResponse(http.StatusOK, map[string]string{"content-type": "application/json"}, string(data))

		case method == http.MethodPost && path == httpbinding.EnvelopePath:
			return dispatchEnvelopeOverHTTP(ctx, builder, req)

		default:
			return jsonResponse(http.StatusNotFound, nil, "not found")
		}
	}
}

// dispatchEnvelopeOverHTTP reimplements httpbinding.EnvelopeHandler's contract (request body is a
// wire.Request, response body is a wire.Response, outer HTTP status always 200 - the real
// outcome travels inside the envelope) for the Lambda HTTP event shape, since awslambda has no
// generic net/http.Handler bridge. This is what the Mesh View's own JS calls (a same-origin
// fetch POST to __ENVELOPE_PATH__) to poll benzene:mesh:query:fleet from a browser.
func dispatchEnvelopeOverHTTP(ctx context.Context, builder *benzene.ApplicationBuilder, req httpEvent) (json.RawMessage, error) {
	body := req.Body
	if req.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, nil, "malformed base64 body")
		}
		body = string(decoded)
	}

	wireReq, err := wire.UnmarshalRequest([]byte(body))
	if err != nil {
		return jsonResponse(http.StatusBadRequest, nil, "malformed envelope: "+err.Error())
	}

	resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, wireReq)
	data, err := wire.MarshalResponse(resp)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, nil, "failed to serialize envelope response")
	}
	return jsonResponse(http.StatusOK, map[string]string{"content-type": "application/json"}, string(data))
}

func flattenHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = h.Get(k)
	}
	return out
}

func main() {
	collector := meshd.New(meshd.Options{})
	log.Printf("mesh collector starting (view %s, envelope %s)", meshd.ViewPath, httpbinding.EnvelopePath)
	awslambda.Start(newMeshHandler(collector))
}
