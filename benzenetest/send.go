package benzenetest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awsdynamodb"
	"github.com/daniellepelley/benzene-go/awskinesis"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/gcppubsub"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/wire"
)

// HTTPResponse is the framework-mapped response the HTTP-shaped hosts return: the real HTTP
// status code (mapped from the handler's Benzene status via httpstatus), the response headers,
// and the body. Every HTTP-shaped Send* (API Gateway, Pub/Sub push, Azure HTTP) returns this,
// so an assertion reads the same regardless of cloud.
type HTTPResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       string
}

// awsHTTPResponse mirrors the API Gateway v2 / Function URL response awslambda.HTTPHandler
// emits.
type awsHTTPResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

// SendAPIGateway pushes an API Gateway HTTP API v2.0 / Function URL request for method+path
// (with payload as the body and optional headers) through the AWS Lambda HTTP binding, and
// returns the native, framework-mapped HTTP response. Topic comes from the route table supplied
// via WithRoutes. This is the AWS front-door analogue of the reference
// ApiGatewayProxyRequestBuilder + SendEventAsync<APIGatewayProxyResponse>.
func SendAPIGateway(t TB, host *Host, method, path string, payload any, headers map[string]string) HTTPResponse {
	t.Helper()
	event := NewAPIGatewayEvent(t, method, path, payload, headers)
	raw, err := awslambda.HTTPHandler(host.builder, host.routes)(context.Background(), event)
	if err != nil {
		t.Fatalf("benzenetest: SendAPIGateway dispatch error: %v", err)
	}
	var resp awsHTTPResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("benzenetest: decode API Gateway response: %v; response = %s", err, raw)
	}
	return HTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Headers, Body: resp.Body}
}

// SendHTTP pushes a native net/http request for method+path (payload as the body, optional
// headers) through the real httpbinding.Handler on host, and returns the native, framework-mapped
// HTTP response (real status code via httpstatus, response headers, body). Topic comes from the
// route table supplied via WithRoutes. This is the native-HTTP front door - the direct net/http
// analogue of SendAPIGateway (API Gateway) and SendAzureHTTP (Azure Functions HTTP): parallel in
// name, argument order, and return shape; only the Send* call changes between them.
func SendHTTP(t TB, host *Host, method, path string, payload any, headers map[string]string) HTTPResponse {
	t.Helper()
	req := NewHTTPRequest(t, method, path, payload, headers)
	return serveHTTPRequest(t, httpbinding.Handler(host.builder, host.routes), req)
}

// NewHTTPRequest builds the native net/http request for method+path carrying payload as the body,
// with headers as HTTP request headers - the shape httpbinding.Handler parses. Topic comes from
// the route table (WithRoutes), not the request. This is the HTTP binding's native-event builder;
// unlike the JSON-envelope builders in events.go it returns an *http.Request, the HTTP binding's
// own native shape.
func NewHTTPRequest(t TB, method, path string, payload any, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(marshalBody(t, payload)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// SendEnvelope pushes the raw wire-contracts.md envelope for topic+payload+headers through the
// AWS Lambda envelope binding and returns the native wire.Response - the direct-invocation /
// service-to-service front door, where the response carries the Benzene status (not an HTTP
// code).
func SendEnvelope(t TB, host *Host, topic benzene.Topic, payload any, headers map[string]string) wire.Response {
	t.Helper()
	event := NewEnvelopeEvent(t, topic, payload, headers)
	raw, err := awslambda.EnvelopeHandler(host.builder)(context.Background(), event)
	if err != nil {
		t.Fatalf("benzenetest: SendEnvelope dispatch error: %v", err)
	}
	resp, err := wire.UnmarshalResponse(raw)
	if err != nil {
		t.Fatalf("benzenetest: decode envelope response: %v; response = %s", err, raw)
	}
	return resp
}

// SendPubSub pushes a Google Cloud Pub/Sub push-subscription delivery for topic+payload+headers
// through the gcppubsub binding and returns the native HTTP acknowledgement: 204 (ack) on a
// success status, 500 (nack, redeliver) otherwise. The GCP analogue of SendSQS.
func SendPubSub(t TB, host *Host, topic benzene.Topic, payload any, headers map[string]string) HTTPResponse {
	t.Helper()
	event := NewPubSubEvent(t, topic, payload, headers)
	return serveHTTP(t, gcppubsub.Handler(host.builder), http.MethodPost, "/", event)
}

// SendAzureHTTP pushes an Azure Functions custom-handler invocation for an HTTP-triggered
// function at path (method + payload + optional headers) through the azurefunctions binding and
// returns the framework-mapped HTTP response read out of the Outputs["res"] envelope. Topic
// comes from the route table supplied via WithRoutes (the route Path is the local invocation
// path, e.g. "/Greet"). The Azure analogue of SendAPIGateway.
func SendAzureHTTP(t TB, host *Host, method, path string, payload any, headers map[string]string) HTTPResponse {
	t.Helper()
	event := NewAzureHTTPEvent(t, method, payload, headers)
	outer := serveHTTP(t, azurefunctions.Handler(host.builder, host.routes), method, path, event)
	if outer.StatusCode != http.StatusOK {
		t.Fatalf("benzenetest: Azure custom handler outer status = %d, want 200; body = %s", outer.StatusCode, outer.Body)
	}
	return decodeAzureOutput(t, outer.Body)
}

// SendAzureQueue pushes an Azure Functions custom-handler invocation for a queue-shaped trigger
// (Storage Queue / Service Bus) through the azurefunctions QueueHandler and returns the native
// HTTP acknowledgement: 200 (success) or 500 (fail, redeliver). dataName is the trigger
// binding's function.json "name" (e.g. "queueItem"), and path is that function's local
// invocation path (e.g. "/GreetQueue"). Topic + headers travel as Service Bus application
// properties. The Azure analogue of SendSQS.
func SendAzureQueue(t TB, host *Host, dataName, path string, topic benzene.Topic, payload any, headers map[string]string) HTTPResponse {
	t.Helper()
	event := newAzureQueueEvent(t, dataName, topic, payload, headers)
	return serveHTTP(t, azurefunctions.QueueHandler(host.builder, dataName), http.MethodPost, path, event)
}

// newAzureQueueEvent builds the queue-shaped custom-handler invocation payload: the message body
// under Data[dataName] (the host serializes the trigger input as a JSON string) and the topic +
// headers under Metadata.UserProperties (Service Bus application properties).
func newAzureQueueEvent(t TB, dataName string, topic benzene.Topic, payload any, headers map[string]string) json.RawMessage {
	t.Helper()
	properties := map[string]json.RawMessage{"topic": mustMarshal(t, topic.String())}
	for k, v := range headers {
		properties[k] = mustMarshal(t, v)
	}
	userProperties := mustMarshal(t, properties)
	event := map[string]any{
		"Data":     map[string]json.RawMessage{dataName: mustMarshal(t, marshalBody(t, payload))},
		"Metadata": map[string]json.RawMessage{"UserProperties": userProperties},
	}
	return mustMarshal(t, event)
}

// SendCosmosChangeFeed pushes an Azure Functions custom-handler invocation for a Cosmos DB Change
// Feed trigger through the azurefunctions CosmosHandler and returns the native HTTP
// acknowledgement: 200 (success - the host checkpoints past this batch) or 500 (fail - the host
// redelivers the whole batch). dataName is the trigger binding's function.json "name" (e.g.
// "documents"), path is that function's local invocation path (e.g. "/OrdersChanged"), topic is the
// fixed topic the feed fans into, and documents is the whole batch (typically a slice, delivered as
// one invocation). Unlike SendAzureQueue there is no per-message header channel - the documents are
// the payload.
func SendCosmosChangeFeed(t TB, host *Host, dataName, path string, topic benzene.Topic, documents any) HTTPResponse {
	t.Helper()
	event := NewCosmosChangeFeedEvent(t, dataName, documents)
	return serveHTTP(t, azurefunctions.CosmosHandler(host.builder, topic, dataName), http.MethodPost, path, event)
}

// SendDynamoDBStream pushes a Lambda DynamoDB stream delivery for one change record through the
// awsdynamodb binding and returns the reported batch-item failures (each entry the failing
// record's sequence number) - empty when the record was handled successfully. The topic resolves
// to "{tableName}:{eventName}" and newImage is the plain document (encoded to AttributeValue format
// by the builder). The DynamoDB analogue of SendSQS, for a stream-triggered consumer.
func SendDynamoDBStream(t TB, host *Host, eventName, tableName, sequenceNumber string, newImage any) []string {
	t.Helper()
	event := NewDynamoDBStreamEvent(t, eventName, tableName, sequenceNumber, newImage)
	raw, err := awsdynamodb.Handler(host.builder)(context.Background(), event)
	if err != nil {
		t.Fatalf("benzenetest: SendDynamoDBStream dispatch error: %v", err)
	}
	var resp struct {
		BatchItemFailures []struct {
			ItemIdentifier string `json:"itemIdentifier"`
		} `json:"batchItemFailures"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("benzenetest: decode DynamoDB batch response: %v; response = %s", err, raw)
	}
	failures := make([]string, len(resp.BatchItemFailures))
	for i, f := range resp.BatchItemFailures {
		failures[i] = f.ItemIdentifier
	}
	return failures
}

// SendKinesisStream pushes a Lambda Kinesis stream delivery for one record through the awskinesis
// binding and returns the reported batch-item failures (each entry the failing record's sequence
// number) - empty when the record was handled successfully. The topic resolves to the stream name
// and payload is the plain record body (marshaled and base64-encoded by the builder). The Kinesis
// analogue of SendDynamoDBStream, for a stream-triggered consumer.
func SendKinesisStream(t TB, host *Host, streamName, sequenceNumber string, payload any) []string {
	t.Helper()
	event := NewKinesisStreamEvent(t, streamName, sequenceNumber, payload)
	raw, err := awskinesis.Handler(host.builder)(context.Background(), event)
	if err != nil {
		t.Fatalf("benzenetest: SendKinesisStream dispatch error: %v", err)
	}
	var resp struct {
		BatchItemFailures []struct {
			ItemIdentifier string `json:"itemIdentifier"`
		} `json:"batchItemFailures"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("benzenetest: decode Kinesis batch response: %v; response = %s", err, raw)
	}
	failures := make([]string, len(resp.BatchItemFailures))
	for i, f := range resp.BatchItemFailures {
		failures[i] = f.ItemIdentifier
	}
	return failures
}

// serveHTTP drives an http.Handler binding with an in-memory request/response and returns the
// outer HTTP response - the credential-free, network-free counterpart of a real cloud HTTP
// delivery.
func serveHTTP(t TB, handler http.Handler, method, path string, body json.RawMessage) HTTPResponse {
	t.Helper()
	return serveHTTPRequest(t, handler, httptest.NewRequest(method, path, strings.NewReader(string(body))))
}

// serveHTTPRequest drives an http.Handler binding with an already-built in-memory request and
// returns the outer HTTP response - the credential-free, network-free counterpart of a real cloud
// HTTP delivery. SendHTTP uses it directly (so request headers ride on the native *http.Request);
// serveHTTP wraps it for the JSON-body bindings that carry their headers inside the event.
func serveHTTPRequest(t TB, handler http.Handler, req *http.Request) HTTPResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	headers := make(map[string]string, len(rec.Header()))
	for name := range rec.Header() {
		headers[strings.ToLower(name)] = rec.Header().Get(name)
	}
	return HTTPResponse{StatusCode: rec.Code, Headers: headers, Body: rec.Body.String()}
}

// decodeAzureOutput reads the framework-mapped HTTP response out of the Azure custom-handler
// Outputs["res"] envelope.
func decodeAzureOutput(t TB, body string) HTTPResponse {
	t.Helper()
	var envelope struct {
		Outputs map[string]struct {
			StatusCode string            `json:"statusCode"`
			Body       string            `json:"body"`
			Headers    map[string]string `json:"headers"`
		} `json:"Outputs"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("benzenetest: decode Azure output envelope: %v; body = %s", err, body)
	}
	res := envelope.Outputs["res"]
	status, err := strconv.Atoi(res.StatusCode)
	if err != nil {
		t.Fatalf("benzenetest: Azure output statusCode %q is not numeric: %v", res.StatusCode, err)
	}
	return HTTPResponse{StatusCode: status, Headers: res.Headers, Body: res.Body}
}
