package benzenetest

import (
	"encoding/base64"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// This file holds the native-event builders: one per transport, each turning a Benzene-level
// (topic, payload, headers) into a message in that transport's own wire shape. They are pure
// JSON construction with no cloud-SDK dependency (the inbound bindings parse plain JSON), so
// they live together here in the neutral package and stay byte-parallel across transports. The
// Send* helpers (send.go, and awssqs/awssns in their own modules) use them; a test rarely calls
// them directly, but they are exported for the occasional hand-rolled dispatch.
//
// payload is serialized by marshalBody: a string / []byte / json.RawMessage is used verbatim,
// anything else is JSON-marshalled. headers may be nil.

// marshalBody renders payload into the string body every wire shape carries.
func marshalBody(t TB, payload any) string {
	t.Helper()
	switch v := payload.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case json.RawMessage:
		return string(v)
	default:
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("benzenetest: marshal payload of type %T: %v", payload, err)
			return ""
		}
		return string(body)
	}
}

// NewSQSEvent builds the Lambda SQS event-source-mapping payload for one record carrying topic
// (as the "topic" message attribute) + payload, with headers as additional attributes - the
// shape awssqs.Handler parses. messageID identifies the record in a batch-item-failure report.
func NewSQSEvent(t TB, messageID string, topic benzene.Topic, payload any, headers map[string]string) json.RawMessage {
	t.Helper()
	attributes := map[string]map[string]string{
		"topic": {"stringValue": topic.String()},
	}
	for k, v := range headers {
		attributes[k] = map[string]string{"stringValue": v}
	}
	event := map[string]any{
		"Records": []map[string]any{{
			"messageId":         messageID,
			"body":              marshalBody(t, payload),
			"messageAttributes": attributes,
		}},
	}
	return mustMarshal(t, event)
}

// NewSNSEvent builds the Lambda SNS notification payload for one record carrying topic (as the
// "topic" message attribute) + payload, with headers as additional attributes - the shape
// awssns.Handler parses.
func NewSNSEvent(t TB, messageID string, topic benzene.Topic, payload any, headers map[string]string) json.RawMessage {
	t.Helper()
	attributes := map[string]map[string]string{
		"topic": {"Value": topic.String()},
	}
	for k, v := range headers {
		attributes[k] = map[string]string{"Value": v}
	}
	event := map[string]any{
		"Records": []map[string]any{{
			"Sns": map[string]any{
				"MessageId":         messageID,
				"Message":           marshalBody(t, payload),
				"MessageAttributes": attributes,
			},
		}},
	}
	return mustMarshal(t, event)
}

// NewAPIGatewayEvent builds the API Gateway HTTP API v2.0 / Lambda Function URL request payload
// for method+path carrying payload, with headers as HTTP headers - the shape
// awslambda.HTTPHandler parses. Topic comes from the route table, not the event.
func NewAPIGatewayEvent(t TB, method, path string, payload any, headers map[string]string) json.RawMessage {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	event := map[string]any{
		"rawPath": path,
		"headers": headers,
		"requestContext": map[string]any{
			"http": map[string]any{"method": method, "path": path},
		},
		"body": marshalBody(t, payload),
	}
	return mustMarshal(t, event)
}

// NewPubSubEvent builds the Google Cloud Pub/Sub push-subscription delivery envelope for topic
// (as the "topic" attribute) + payload, with headers as additional attributes - the shape
// gcppubsub.Handler parses. The data is base64-encoded, as Pub/Sub delivers it.
func NewPubSubEvent(t TB, topic benzene.Topic, payload any, headers map[string]string) json.RawMessage {
	t.Helper()
	attributes := map[string]string{"topic": topic.String()}
	for k, v := range headers {
		attributes[k] = v
	}
	event := map[string]any{
		"message": map[string]any{
			"data":       base64.StdEncoding.EncodeToString([]byte(marshalBody(t, payload))),
			"attributes": attributes,
		},
		"subscription": "projects/test-project/subscriptions/test-subscription",
	}
	return mustMarshal(t, event)
}

// NewEnvelopeEvent builds the raw wire-contracts.md envelope for topic + payload + headers - the
// shape awslambda.EnvelopeHandler (and any envelope-over-transport entry point) parses.
func NewEnvelopeEvent(t TB, topic benzene.Topic, payload any, headers map[string]string) json.RawMessage {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	data, err := wire.MarshalRequest(wire.Request{Topic: topic.String(), Headers: headers, Body: marshalBody(t, payload)})
	if err != nil {
		t.Fatalf("benzenetest: marshal envelope request: %v", err)
	}
	return data
}

// NewAzureHTTPEvent builds the Azure Functions custom-handler invocation payload for an
// HTTP-triggered function carrying method + payload, with headers as HTTP headers - the shape
// azurefunctions.Handler parses out of Data["req"]. The path travels on the HTTP request itself
// (see SendAzureHTTP), not in this body.
func NewAzureHTTPEvent(t TB, method string, payload any, headers map[string]string) json.RawMessage {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	req, err := json.Marshal(map[string]any{"Method": method, "Headers": headers, "Body": marshalBody(t, payload)})
	if err != nil {
		t.Fatalf("benzenetest: marshal azure http trigger: %v", err)
	}
	event := map[string]any{
		"Data":     map[string]json.RawMessage{"req": req},
		"Metadata": map[string]json.RawMessage{},
	}
	return mustMarshal(t, event)
}

// NewCosmosChangeFeedEvent builds the Azure Functions custom-handler invocation payload for a
// Cosmos DB Change Feed trigger: the batch of changed documents under Data[dataName] as a JSON
// array - the shape azurefunctions.CosmosHandler parses. documents is the whole batch (typically a
// slice); the change feed is fan-in, so it is one event, not one per document, and there is no
// topic or header channel (the documents are the payload).
func NewCosmosChangeFeedEvent(t TB, dataName string, documents any) json.RawMessage {
	t.Helper()
	event := map[string]any{
		"Data": map[string]json.RawMessage{dataName: json.RawMessage(marshalBody(t, documents))},
	}
	return mustMarshal(t, event)
}

func mustMarshal(t TB, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("benzenetest: marshal event: %v", err)
	}
	return data
}
