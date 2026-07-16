package azurefunctions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daniellepelley/benzene-go/wire"
)

func invokeQueue(t *testing.T, handler http.Handler, hostPayload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/GreetQueue", strings.NewReader(hostPayload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestQueueHandler_EnvelopeInBodyDispatches(t *testing.T) {
	// A Storage Queue message has no attributes, so the topic travels as a full wire
	// envelope in the message body - which the host delivers as a JSON *string* under the
	// trigger binding's name.
	handler := QueueHandler(newTestBuilder(t), "queueItem")

	message, err := json.Marshal(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"Queue"}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	hostPayload, err := json.Marshal(map[string]any{
		"Data":     map[string]any{"queueItem": string(message)},
		"Metadata": map[string]any{"Id": "message-1", "DequeueCount": "1"},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	rec := invokeQueue(t, handler, string(hostPayload))
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp queueSuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rec.Body.String())
	}
	if resp.Outputs == nil {
		t.Error("Outputs should be present (empty object), not absent")
	}
}

func TestQueueHandler_UserPropertiesTopicDispatches(t *testing.T) {
	// A Service Bus message carries the topic as an application property (UserProperties) -
	// the native-attribute transport of wire-contracts.md §2 - and the body is the raw
	// request payload.
	handler := QueueHandler(newTestBuilder(t), "mySbMsg")

	hostPayload := `{
		"Data": {"mySbMsg": "{\"name\":\"ServiceBus\"}"},
		"Metadata": {"UserProperties": {"topic": "greet", "x-correlation-id": "abc", "retryCount": 3}}
	}`

	rec := invokeQueue(t, handler, hostPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestQueueHandler_FailedDispatchIsOuterFailure(t *testing.T) {
	// Unlike the HTTP Handler's outer-200 convention, a queue-shaped trigger reports a failed
	// message via a non-2xx outer status so the platform redelivers it.
	tests := []struct {
		name        string
		hostPayload string
	}{
		{
			name:        "handler failure status",
			hostPayload: `{"Data": {"queueItem": "{\"topic\":\"greet\",\"headers\":{},\"body\":\"{\\\"name\\\":\\\"\\\"}\"}"}, "Metadata": {}}`,
		},
		{
			name:        "no topic resolvable",
			hostPayload: `{"Data": {"queueItem": "just some text"}, "Metadata": {}}`,
		},
		{
			name:        "trigger binding name absent from Data",
			hostPayload: `{"Data": {}, "Metadata": {}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := QueueHandler(newTestBuilder(t), "queueItem")
			rec := invokeQueue(t, handler, tt.hostPayload)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
			}
			var payload wire.ErrorPayload
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("json.Unmarshal(error payload) error = %v; body = %s", err, rec.Body.String())
			}
			if payload.Status == "" {
				t.Errorf("error payload Status is empty; body = %s", rec.Body.String())
			}
		})
	}
}

func TestQueueHandler_MalformedHostPayloadIsBadRequest(t *testing.T) {
	handler := QueueHandler(newTestBuilder(t), "queueItem")
	rec := invokeQueue(t, handler, "{not valid json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestQueueHandler_BodyReadErrorIsOuterBadRequest(t *testing.T) {
	handler := QueueHandler(newTestBuilder(t), "queueItem")

	req := httptest.NewRequest(http.MethodPost, "/GreetQueue", nil)
	req.Body = errReadCloser{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestResolveQueueRequest(t *testing.T) {
	tests := []struct {
		name        string
		inv         queueInvocationRequest
		dataName    string
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name: "UserProperties topic wins over envelope body",
			inv: queueInvocationRequest{
				Data:     map[string]json.RawMessage{"m": json.RawMessage(`"{\"topic\":\"other\",\"headers\":{},\"body\":\"x\"}"`)},
				Metadata: map[string]json.RawMessage{"UserProperties": json.RawMessage(`{"Topic":"greet"}`)},
			},
			dataName:    "m",
			wantTopic:   "greet",
			wantBody:    `{"topic":"other","headers":{},"body":"x"}`,
			wantHeaders: map[string]string{},
		},
		{
			name: "envelope headers merge with property headers",
			inv: queueInvocationRequest{
				Data:     map[string]json.RawMessage{"m": json.RawMessage(`"{\"topic\":\"greet\",\"headers\":{\"from-envelope\":\"e\"},\"body\":\"{}\"}"`)},
				Metadata: map[string]json.RawMessage{"UserProperties": json.RawMessage(`{"from-properties":"p"}`)},
			},
			dataName:    "m",
			wantTopic:   "greet",
			wantBody:    `{}`,
			wantHeaders: map[string]string{"from-envelope": "e", "from-properties": "p"},
		},
		{
			name: "JSON object message is kept verbatim as the body",
			inv: queueInvocationRequest{
				Data:     map[string]json.RawMessage{"m": json.RawMessage(`{"name":"Object"}`)},
				Metadata: map[string]json.RawMessage{"UserProperties": json.RawMessage(`{"topic":"greet"}`)},
			},
			dataName:    "m",
			wantTopic:   "greet",
			wantBody:    `{"name":"Object"}`,
			wantHeaders: map[string]string{},
		},
		{
			name: "malformed UserProperties degrades to envelope resolution",
			inv: queueInvocationRequest{
				Data:     map[string]json.RawMessage{"m": json.RawMessage(`"{\"topic\":\"greet\",\"headers\":{},\"body\":\"{}\"}"`)},
				Metadata: map[string]json.RawMessage{"UserProperties": json.RawMessage(`"not an object"`)},
			},
			dataName:    "m",
			wantTopic:   "greet",
			wantBody:    `{}`,
			wantHeaders: map[string]string{},
		},
		{
			name: "nothing resolvable yields empty topic and raw body",
			inv: queueInvocationRequest{
				Data:     map[string]json.RawMessage{"m": json.RawMessage(`"plain text"`)},
				Metadata: nil,
			},
			dataName:    "m",
			wantTopic:   "",
			wantBody:    "plain text",
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := resolveQueueRequest(tt.inv, tt.dataName)
			if req.Topic != tt.wantTopic {
				t.Errorf("Topic = %q, want %q", req.Topic, tt.wantTopic)
			}
			if req.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q", req.Body, tt.wantBody)
			}
			if len(req.Headers) != len(tt.wantHeaders) {
				t.Fatalf("Headers = %v, want %v", req.Headers, tt.wantHeaders)
			}
			for k, v := range tt.wantHeaders {
				if req.Headers[k] != v {
					t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
				}
			}
		})
	}
}
