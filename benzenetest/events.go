package benzenetest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

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
// Cosmos DB Change Feed trigger: the batch of changed documents under Data[dataName] - the shape
// azurefunctions.CosmosHandler parses. documents is the whole batch (typically a slice); the change
// feed is fan-in, so it is one event, not one per document, and there is no topic or header channel
// (the documents are the payload). The batch is delivered as a JSON *string* wrapping the array,
// the same double-encoding the Functions host applies to a trigger input (and that newAzureQueueEvent
// reproduces), so an adopter's test exercises the binding's string-unwrap path, not just a raw array.
func NewCosmosChangeFeedEvent(t TB, dataName string, documents any) json.RawMessage {
	t.Helper()
	event := map[string]any{
		"Data": map[string]json.RawMessage{dataName: mustMarshal(t, marshalBody(t, documents))},
	}
	return mustMarshal(t, event)
}

// NewDynamoDBStreamEvent builds the Lambda DynamoDB stream event-source-mapping payload for one
// change record - the shape awsdynamodb.Handler parses. The topic it resolves to is
// "{tableName}:{eventName}" (e.g. "orders:INSERT"); document is the plain row (a struct or map),
// which this encodes into DynamoDB AttributeValue format so the binding round-trips it back to plain
// JSON for the handler. It is placed under the image key that matches the change type - OldImage for
// a REMOVE (a delete carries no new image), NewImage otherwise - so the helper reproduces the real
// record shape and exercises the binding's NewImage-else-OldImage-else-Keys fallback.
// sequenceNumber identifies the record in a batch-item-failure report.
func NewDynamoDBStreamEvent(t TB, eventName, tableName, sequenceNumber string, document any) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(marshalBody(t, document)), &fields); err != nil {
		t.Fatalf("benzenetest: NewDynamoDBStreamEvent document must be a JSON object: %v", err)
	}
	image := make(map[string]any, len(fields))
	for name, value := range fields {
		image[name] = jsonToAttributeValue(t, value)
	}
	imageKey := "NewImage"
	if eventName == "REMOVE" {
		imageKey = "OldImage"
	}
	arn := "arn:aws:dynamodb:us-east-1:000000000000:table/" + tableName + "/stream/2024-01-01T00:00:00.000"
	event := map[string]any{
		"Records": []map[string]any{{
			"eventID":        "evt-" + sequenceNumber,
			"eventName":      eventName,
			"eventSource":    "aws:dynamodb",
			"eventSourceARN": arn,
			"awsRegion":      "us-east-1",
			"dynamodb": map[string]any{
				imageKey:         image,
				"SequenceNumber": sequenceNumber,
				"StreamViewType": "NEW_AND_OLD_IMAGES",
			},
		}},
	}
	return mustMarshal(t, event)
}

// jsonToAttributeValue encodes one plain-JSON value into its DynamoDB AttributeValue wrapper - the
// inverse of the awsdynamodb binding's converter, used only to build test events. Number literals
// pass through verbatim (as the "N" string) so integer precision is preserved.
func jsonToAttributeValue(t TB, raw json.RawMessage) any {
	t.Helper()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]any{"NULL": true}
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			t.Fatalf("benzenetest: encode AttributeValue map: %v", err)
		}
		m := make(map[string]any, len(obj))
		for name, value := range obj {
			m[name] = jsonToAttributeValue(t, value)
		}
		return map[string]any{"M": m}
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			t.Fatalf("benzenetest: encode AttributeValue list: %v", err)
		}
		list := make([]any, len(arr))
		for i, value := range arr {
			list[i] = jsonToAttributeValue(t, value)
		}
		return map[string]any{"L": list}
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			t.Fatalf("benzenetest: encode AttributeValue string: %v", err)
		}
		return map[string]any{"S": s}
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(trimmed, &b); err != nil {
			t.Fatalf("benzenetest: encode AttributeValue bool: %v", err)
		}
		return map[string]any{"BOOL": b}
	default:
		return map[string]any{"N": string(trimmed)}
	}
}

// NewKinesisStreamEvent builds the Lambda Kinesis stream event-source-mapping payload for one
// record - the shape awskinesis.Handler parses. The topic it resolves to is the stream name (e.g.
// "orders"); payload is the plain record body, which this marshals and base64-encodes into the
// record's data field (as Lambda delivers it) so the binding decodes it back for the handler.
// sequenceNumber identifies the record in a batch-item-failure report.
func NewKinesisStreamEvent(t TB, streamName, sequenceNumber string, payload any) json.RawMessage {
	t.Helper()
	arn := "arn:aws:kinesis:us-east-1:000000000000:stream/" + streamName
	event := map[string]any{
		"Records": []map[string]any{{
			"eventID":        "evt-" + sequenceNumber,
			"eventName":      "aws:kinesis:record",
			"eventSource":    "aws:kinesis",
			"eventSourceARN": arn,
			"awsRegion":      "us-east-1",
			"kinesis": map[string]any{
				"partitionKey":   "pk-" + sequenceNumber,
				"sequenceNumber": sequenceNumber,
				"data":           base64.StdEncoding.EncodeToString([]byte(marshalBody(t, payload))),
			},
		}},
	}
	return mustMarshal(t, event)
}

// NewS3Event builds the Lambda S3 event-notification payload for one record - the shape
// awss3.Handler parses. The topic it resolves to is "{bucket}:{eventName}" (e.g.
// "uploads:ObjectCreated:Put"); the object metadata (key, a fixed size and etag) becomes the body.
func NewS3Event(t TB, bucket, eventName, key string) json.RawMessage {
	t.Helper()
	event := map[string]any{
		"Records": []map[string]any{{
			"eventSource": "aws:s3",
			"eventName":   eventName,
			"awsRegion":   "us-east-1",
			"s3": map[string]any{
				"bucket": map[string]any{"name": bucket},
				"object": map[string]any{"key": key, "size": 42, "eTag": "test-etag"},
			},
		}},
	}
	return mustMarshal(t, event)
}

// NewTimerEvent builds the Azure Functions custom-handler invocation payload for a Timer trigger
// tick: the tick's schedule info under Data[dataName] - the shape azurefunctions.TimerHandler
// parses. tick is the schedule info (e.g. a struct with IsPastDue), or nil for a handler that
// ignores it. It is delivered as a JSON string wrapping the object, the same double-encoding the
// Functions host applies to a trigger input (and that NewCosmosChangeFeedEvent reproduces).
func NewTimerEvent(t TB, dataName string, tick any) json.RawMessage {
	t.Helper()
	event := map[string]any{
		"Data": map[string]json.RawMessage{dataName: mustMarshal(t, marshalBody(t, tick))},
	}
	return mustMarshal(t, event)
}

// NewKafkaEvent builds the Lambda MSK/Kafka event-source-mapping payload for one record - the shape
// awskafka.Handler parses. The Benzene topic it resolves to is the Kafka topic; payload is the plain
// record body, marshaled and base64-encoded into the record's value (as Lambda delivers it). The
// record is filed under the "{topic}-{partition}" group key at the given offset.
func NewKafkaEvent(t TB, topic string, partition int, offset int64, payload any) json.RawMessage {
	t.Helper()
	event := map[string]any{
		"eventSource": "aws:kafka",
		"records": map[string]any{
			fmt.Sprintf("%s-%d", topic, partition): []map[string]any{{
				"topic":     topic,
				"partition": partition,
				"offset":    offset,
				"timestamp": 1700000000000,
				"value":     base64.StdEncoding.EncodeToString([]byte(marshalBody(t, payload))),
			}},
		},
	}
	return mustMarshal(t, event)
}

// NewEventGridEvent builds the Azure Functions custom-handler invocation payload for an Event Grid
// trigger: one Event Grid-schema event under Data[dataName] - the shape azurefunctions.EventGridHandler
// parses. eventType is the event's type (which the binding resolves as the Benzene topic); payload is
// the event's data. The event is delivered as a JSON object (the way the host forwards a structured
// trigger input); the binding also accepts a string-wrapped object, exercised in its own tests.
func NewEventGridEvent(t TB, dataName, eventType string, payload any) json.RawMessage {
	t.Helper()
	event := map[string]any{
		"id":        "evt-1",
		"eventType": eventType,
		"subject":   eventType,
		"topic":     "/subscriptions/test/resourceGroups/test",
		"eventTime": "2024-01-01T00:00:00Z",
		"data":      json.RawMessage(marshalBody(t, payload)),
	}
	return mustMarshal(t, map[string]any{"Data": map[string]any{dataName: event}})
}

func mustMarshal(t TB, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("benzenetest: marshal event: %v", err)
	}
	return data
}
