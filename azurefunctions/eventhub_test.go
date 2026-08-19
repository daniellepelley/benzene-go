package azurefunctions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// newEventHubBuilder wires a service whose "greet" handler records the order it saw requests in,
// so a test can assert per-event (not per-batch) dispatch and its ordering.
func newEventHubBuilder(t *testing.T, seen *[]greetRequest) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](
		func(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
			if seen != nil {
				*seen = append(*seen, req)
			}
			if req.Name == "" {
				return benzene.BadRequest[greetResponse]("name is required")
			}
			return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
		})); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func invokeEventHub(t *testing.T, handler http.Handler, hostPayload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/OrderPlaced", strings.NewReader(hostPayload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestEventHubHandler_BatchDispatchesOnePipelineRunPerEvent(t *testing.T) {
	var seen []greetRequest
	handler := EventHubHandler(newEventHubBuilder(t, &seen), "eventHubMessages")

	hostPayload := `{
		"Data": {"eventHubMessages": ["{\"name\":\"Alice\"}", "{\"name\":\"Bob\"}"]},
		"Metadata": {"PropertiesArray": [{"topic": "greet"}, {"topic": "greet"}]}
	}`

	rec := invokeEventHub(t, handler, hostPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 2 {
		t.Fatalf("pipeline ran %d times, want exactly 2 (one invocation per event)", len(seen))
	}
	if seen[0].Name != "Alice" || seen[1].Name != "Bob" {
		t.Errorf("dispatch order = %+v, want Alice then Bob (batch order preserved)", seen)
	}
	var resp queueSuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rec.Body.String())
	}
	if resp.Outputs == nil {
		t.Error("Outputs should be present (empty object), not absent")
	}
}

func TestEventHubHandler_EnvelopeInBodyDispatches(t *testing.T) {
	// With no PropertiesArray at all (a producer that never set application properties), the
	// topic travels as a full wire envelope in the event body.
	var seen []greetRequest
	handler := EventHubHandler(newEventHubBuilder(t, &seen), "eventHubMessages")

	message, err := json.Marshal(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"Envelope"}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	hostPayload, err := json.Marshal(map[string]any{
		"Data": map[string]any{"eventHubMessages": []string{string(message)}},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	rec := invokeEventHub(t, handler, string(hostPayload))
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 1 || seen[0].Name != "Envelope" {
		t.Errorf("seen = %+v, want a single Envelope request", seen)
	}
}

func TestEventHubHandler_StopsAtFirstFailureAndSkipsLaterEvents(t *testing.T) {
	// Event Hubs' batch-level checkpoint means a failure anywhere redelivers the whole batch
	// regardless; EventHubHandler stops at the first failure rather than running (and
	// side-effecting) events downstream of one that is already doomed to redeliver.
	var seen []greetRequest
	handler := EventHubHandler(newEventHubBuilder(t, &seen), "eventHubMessages")

	hostPayload := `{
		"Data": {"eventHubMessages": ["{\"name\":\"\"}", "{\"name\":\"NeverReached\"}"]},
		"Metadata": {"PropertiesArray": [{"topic": "greet"}, {"topic": "greet"}]}
	}`

	rec := invokeEventHub(t, handler, hostPayload)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var payload wire.ErrorPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal(error payload) error = %v; body = %s", err, rec.Body.String())
	}
	if payload.BenzeneStatus == "" {
		t.Errorf("error payload benzeneStatus is empty; body = %s", rec.Body.String())
	}
	if len(seen) != 1 {
		t.Fatalf("pipeline ran %d times, want exactly 1 (stopped before the second event)", len(seen))
	}
}

func TestEventHubHandler_NoTopicResolvableIsOuterFailure(t *testing.T) {
	handler := EventHubHandler(newEventHubBuilder(t, nil), "eventHubMessages")

	rec := invokeEventHub(t, handler, `{"Data": {"eventHubMessages": ["just some text"]}, "Metadata": {}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

func TestEventHubHandler_EmptyBatchIsOuterSuccess(t *testing.T) {
	tests := []struct {
		name        string
		hostPayload string
	}{
		{name: "trigger binding name absent from Data", hostPayload: `{"Data": {}, "Metadata": {}}`},
		{name: "present-but-null value", hostPayload: `{"Data": {"eventHubMessages": null}, "Metadata": {}}`},
		{name: "empty array", hostPayload: `{"Data": {"eventHubMessages": []}, "Metadata": {}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen []greetRequest
			handler := EventHubHandler(newEventHubBuilder(t, &seen), "eventHubMessages")
			rec := invokeEventHub(t, handler, tt.hostPayload)
			if rec.Code != http.StatusOK {
				t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if len(seen) != 0 {
				t.Errorf("pipeline ran %d times, want 0 for an empty batch", len(seen))
			}
		})
	}
}

func TestEventHubHandler_StringWrappedArrayIsUnwrapped(t *testing.T) {
	// The host may deliver the batch as a JSON *string* wrapping the array (the same
	// double-encoding QueueHandler/CosmosHandler handle) rather than the array directly.
	var seen []greetRequest
	handler := EventHubHandler(newEventHubBuilder(t, &seen), "eventHubMessages")

	hostPayload := `{
		"Data": {"eventHubMessages": "[\"{\\\"name\\\":\\\"Wrapped\\\"}\"]"},
		"Metadata": {"PropertiesArray": [{"topic": "greet"}]}
	}`

	rec := invokeEventHub(t, handler, hostPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 1 || seen[0].Name != "Wrapped" {
		t.Errorf("seen = %+v, want a single Wrapped request", seen)
	}
}

func TestEventHubHandler_ShorterPropertiesArrayFallsBackForLaterEvents(t *testing.T) {
	// A PropertiesArray shorter than the batch (or absent for some other reason) must not fail
	// the whole batch - the events it doesn't cover simply fall through to envelope-in-body
	// resolution.
	var seen []greetRequest
	handler := EventHubHandler(newEventHubBuilder(t, &seen), "eventHubMessages")

	message, err := json.Marshal(wire.Request{Topic: "greet", Headers: map[string]string{}, Body: `{"name":"Second"}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	hostPayload, err := json.Marshal(map[string]any{
		"Data":     map[string]any{"eventHubMessages": []string{`{"name":"First"}`, string(message)}},
		"Metadata": map[string]any{"PropertiesArray": []map[string]string{{"topic": "greet"}}},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	rec := invokeEventHub(t, handler, string(hostPayload))
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 2 || seen[0].Name != "First" || seen[1].Name != "Second" {
		t.Errorf("seen = %+v, want First (via properties) then Second (via envelope-in-body)", seen)
	}
}

func TestEventHubHandler_MalformedHostPayloadIsBadRequest(t *testing.T) {
	handler := EventHubHandler(newEventHubBuilder(t, nil), "eventHubMessages")
	rec := invokeEventHub(t, handler, "{not valid json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestEventHubHandler_BodyReadErrorIsOuterBadRequest(t *testing.T) {
	handler := EventHubHandler(newEventHubBuilder(t, nil), "eventHubMessages")

	req := httptest.NewRequest(http.MethodPost, "/OrderPlaced", nil)
	req.Body = errReadCloser{}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestEventHubHandler_MalformedArrayIsEmptyBatch(t *testing.T) {
	// Data[dataName] present but not a JSON array at all (nor a string wrapping one) degrades to
	// an empty batch rather than a hard failure - the same forgiving stance
	// resolveCosmosBatch/resolveTimerBody take on an unexpected shape.
	var seen []greetRequest
	handler := EventHubHandler(newEventHubBuilder(t, &seen), "eventHubMessages")

	rec := invokeEventHub(t, handler, `{"Data": {"eventHubMessages": {"not":"an array"}}, "Metadata": {}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(seen) != 0 {
		t.Errorf("pipeline ran %d times, want 0", len(seen))
	}
}

func TestResolveEventHubRequest(t *testing.T) {
	tests := []struct {
		name        string
		eventBody   json.RawMessage
		properties  map[string]json.RawMessage
		wantTopic   string
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			name:        "properties topic wins over envelope body",
			eventBody:   json.RawMessage(`"{\"topic\":\"other\",\"headers\":{},\"body\":\"x\"}"`),
			properties:  map[string]json.RawMessage{"Topic": json.RawMessage(`"greet"`)},
			wantTopic:   "greet",
			wantBody:    `{"topic":"other","headers":{},"body":"x"}`,
			wantHeaders: map[string]string{},
		},
		{
			name:        "envelope headers merge with property headers",
			eventBody:   json.RawMessage(`"{\"topic\":\"greet\",\"headers\":{\"from-envelope\":\"e\"},\"body\":\"{}\"}"`),
			properties:  map[string]json.RawMessage{"from-properties": json.RawMessage(`"p"`)},
			wantTopic:   "greet",
			wantBody:    `{}`,
			wantHeaders: map[string]string{"from-envelope": "e", "from-properties": "p"},
		},
		{
			name:        "JSON object event is kept verbatim as the body",
			eventBody:   json.RawMessage(`{"name":"Object"}`),
			properties:  map[string]json.RawMessage{"topic": json.RawMessage(`"greet"`)},
			wantTopic:   "greet",
			wantBody:    `{"name":"Object"}`,
			wantHeaders: map[string]string{},
		},
		{
			// A "topic" property resolves the topic directly (like Service Bus's
			// UserProperties): the body is used verbatim, not parsed as an envelope, and the
			// non-string retryCount property is skipped rather than failing the event.
			name:        "non-string property values are skipped",
			eventBody:   json.RawMessage(`"{\"topic\":\"other\",\"headers\":{},\"body\":\"x\"}"`),
			properties:  map[string]json.RawMessage{"topic": json.RawMessage(`"greet"`), "retryCount": json.RawMessage(`3`)},
			wantTopic:   "greet",
			wantBody:    `{"topic":"other","headers":{},"body":"x"}`,
			wantHeaders: map[string]string{},
		},
		{
			name:        "nothing resolvable yields empty topic and raw body",
			eventBody:   json.RawMessage(`"plain text"`),
			properties:  nil,
			wantTopic:   "",
			wantBody:    "plain text",
			wantHeaders: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := resolveEventHubRequest(tt.eventBody, tt.properties)
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
