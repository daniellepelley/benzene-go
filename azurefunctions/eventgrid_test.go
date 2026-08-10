package azurefunctions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

// errReader always fails, to exercise the body-read-error branch.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// newEventGridBuilder registers a handler on a custom event type ("com.example.orderPlaced") that
// echoes the order id, so a dispatch through EventGridHandler can be asserted on.
func newEventGridBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	type order struct {
		ID string `json:"id"`
	}
	handler := benzene.Handler[order, struct{}](func(_ context.Context, o order) benzene.Result[struct{}] {
		if o.ID == "" {
			return benzene.BadRequest[struct{}]("order id is required")
		}
		return benzene.Ok(struct{}{})
	})
	if err := benzene.Register(registry, benzene.NewTopic("com.example.orderPlaced"), handler); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

// eventGridHostPayload wraps one event (as an object, the way the host forwards a structured trigger
// input) under Data[dataName].
func eventGridHostPayload(t *testing.T, dataName string, event map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"Data": map[string]any{dataName: event}})
	if err != nil {
		t.Fatalf("marshal host payload: %v", err)
	}
	return string(payload)
}

func invokeEventGrid(t *testing.T, handler http.Handler, hostPayload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/OrderPlaced", strings.NewReader(hostPayload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestEventGridHandler_EventGridSchemaDispatches(t *testing.T) {
	handler := EventGridHandler(newEventGridBuilder(t), "eventGridEvent")
	payload := eventGridHostPayload(t, "eventGridEvent", map[string]any{
		"id":        "evt-1",
		"eventType": "com.example.orderPlaced",
		"subject":   "orders/o-1",
		"topic":     "/subscriptions/x/resourceGroups/y",
		"data":      map[string]any{"id": "o-1"},
	})
	rec := invokeEventGrid(t, handler, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

func TestEventGridHandler_UnroutedEventTypeFails(t *testing.T) {
	handler := EventGridHandler(newEventGridBuilder(t), "eventGridEvent")
	payload := eventGridHostPayload(t, "eventGridEvent", map[string]any{
		"id":        "evt-2",
		"eventType": "com.example.unhandled",
		"data":      map[string]any{"id": "o-1"},
	})
	rec := invokeEventGrid(t, handler, payload)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("outer status = %d, want 500 (unrouted event type -> Event Grid retry)", rec.Code)
	}
}

func TestEventGridHandler_HandlerFailureIsOuter500(t *testing.T) {
	handler := EventGridHandler(newEventGridBuilder(t), "eventGridEvent")
	// Empty data -> order id missing -> BadRequest -> outer 500.
	payload := eventGridHostPayload(t, "eventGridEvent", map[string]any{
		"eventType": "com.example.orderPlaced",
		"data":      map[string]any{},
	})
	rec := invokeEventGrid(t, handler, payload)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("outer status = %d, want 500 (handler rejected the event)", rec.Code)
	}
}

func TestEventGridHandler_StringWrappedEventIsUnwrapped(t *testing.T) {
	handler := EventGridHandler(newEventGridBuilder(t), "eventGridEvent")
	// The host may deliver the event as a JSON string wrapping the object (double-encoding).
	eventJSON, err := json.Marshal(map[string]any{"eventType": "com.example.orderPlaced", "data": map[string]any{"id": "o-1"}})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"Data": map[string]any{"eventGridEvent": string(eventJSON)}})
	if err != nil {
		t.Fatalf("marshal host payload: %v", err)
	}
	rec := invokeEventGrid(t, handler, string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

func TestEventGridHandler_MalformedInvocationIs400(t *testing.T) {
	handler := EventGridHandler(newEventGridBuilder(t), "eventGridEvent")
	rec := invokeEventGrid(t, handler, "not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want 400 for a malformed invocation payload", rec.Code)
	}
}

func TestEventGridHandler_BodyReadErrorIs400(t *testing.T) {
	handler := EventGridHandler(newEventGridBuilder(t), "eventGridEvent")
	req := httptest.NewRequest(http.MethodPost, "/OrderPlaced", errReader{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want 400 when the body cannot be read", rec.Code)
	}
}

func TestResolveEventGridRequest_MissingDataIsEmptyTopic(t *testing.T) {
	req := resolveEventGridRequest(eventGridInvocationRequest{Data: map[string]json.RawMessage{}}, "eventGridEvent")
	if req.Topic != "" {
		t.Errorf("Topic = %q, want empty (missing event -> ValidationError, never dropped)", req.Topic)
	}
}

func TestParseEventGridEvent_EventGridSchema(t *testing.T) {
	req := parseEventGridEvent([]byte(`{
		"id": "evt-1",
		"eventType": "Microsoft.Storage.BlobCreated",
		"subject": "/blobServices/default/containers/c/blobs/b",
		"topic": "/subscriptions/x",
		"data": {"url": "https://example/b"}
	}`))
	if req.Topic != "Microsoft.Storage.BlobCreated" {
		t.Errorf("Topic = %q, want the eventType", req.Topic)
	}
	if req.Headers["id"] != "evt-1" || req.Headers["subject"] == "" || req.Headers["source"] != "/subscriptions/x" {
		t.Errorf("Headers = %v, want id/subject/source from the envelope", req.Headers)
	}
	if req.Body != `{"url": "https://example/b"}` {
		t.Errorf("Body = %q, want the raw data payload", req.Body)
	}
}

func TestParseEventGridEvent_CloudEventsSchema(t *testing.T) {
	req := parseEventGridEvent([]byte(`{
		"specversion": "1.0",
		"id": "evt-2",
		"type": "com.example.orderPlaced",
		"source": "/orders",
		"subject": "o-1",
		"data": {"id": "o-1"}
	}`))
	if req.Topic != "com.example.orderPlaced" {
		t.Errorf("Topic = %q, want the CloudEvents type", req.Topic)
	}
	if req.Headers["source"] != "/orders" {
		t.Errorf("source header = %q, want the CloudEvents source", req.Headers["source"])
	}
}

func TestParseEventGridEvent_DataFallbacks(t *testing.T) {
	t.Run("absent data becomes empty object", func(t *testing.T) {
		req := parseEventGridEvent([]byte(`{"eventType":"t"}`))
		if req.Body != "{}" {
			t.Errorf("Body = %q, want {} when no data", req.Body)
		}
	})
	t.Run("null data becomes empty object", func(t *testing.T) {
		req := parseEventGridEvent([]byte(`{"eventType":"t","data":null}`))
		if req.Body != "{}" {
			t.Errorf("Body = %q, want {} for null data", req.Body)
		}
	})
	t.Run("no envelope fields yields no headers", func(t *testing.T) {
		req := parseEventGridEvent([]byte(`{"eventType":"t"}`))
		if len(req.Headers) != 0 {
			t.Errorf("Headers = %v, want empty when the event carries no id/subject/source", req.Headers)
		}
	})
}

func TestParseEventGridEvent_UnparseableIsEmptyTopic(t *testing.T) {
	req := parseEventGridEvent([]byte(`not an object`))
	if req.Topic != "" {
		t.Errorf("Topic = %q, want empty for an unparseable event", req.Topic)
	}
}
