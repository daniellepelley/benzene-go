package gcpfunctions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/event"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/wire"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func newTestBuilder(t *testing.T, names wire.ReservedNames) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:      registry,
		Container:     benzene.NewContainer(),
		Pipeline:      benzene.NewPipeline(benzene.RouterMiddleware(registry)),
		ReservedNames: names,
	}
}

// newGreetEvent builds a CloudEvent whose type is the "greet" topic and whose data is the
// greet request payload.
func newGreetEvent(t *testing.T, name string) cloudevents.Event {
	t.Helper()
	e := event.New()
	e.SetID("evt-1")
	e.SetSource("//pubsub.googleapis.com/projects/p/topics/greet")
	e.SetType("greet")
	e.SetSubject("greetings")
	if err := e.SetData(cloudevents.ApplicationJSON, greetRequest{Name: name}); err != nil {
		t.Fatalf("SetData() error = %v", err)
	}
	return e
}

func TestDispatchCloudEvent_SuccessReturnsNilAndDispatches(t *testing.T) {
	builder := newTestBuilder(t, wire.ReservedNames{})
	e := newGreetEvent(t, "CloudEvent")

	called := false
	onFailure := func(context.Context, cloudevents.Event, wire.Response) { called = true }

	if err := dispatchCloudEvent(context.Background(), e, builder, builder.ReservedNames, onFailure); err != nil {
		t.Fatalf("dispatchCloudEvent() error = %v, want nil", err)
	}
	if called {
		t.Error("onFailure was called for a successful dispatch")
	}
}

func TestDispatchCloudEvent_UnsuccessfulResultReturnsErrorAndCallsOnFailure(t *testing.T) {
	builder := newTestBuilder(t, wire.ReservedNames{})
	e := newGreetEvent(t, "") // empty name -> BadRequest from the handler

	var gotResp wire.Response
	var gotEvent cloudevents.Event
	calls := 0
	onFailure := func(_ context.Context, ev cloudevents.Event, resp wire.Response) {
		calls++
		gotEvent = ev
		gotResp = resp
	}

	err := dispatchCloudEvent(context.Background(), e, builder, builder.ReservedNames, onFailure)
	if err == nil {
		t.Fatal("dispatchCloudEvent() error = nil, want non-nil for an unsuccessful result")
	}
	if calls != 1 {
		t.Fatalf("onFailure called %d times, want 1", calls)
	}
	if gotEvent.Type() != "greet" {
		t.Errorf("onFailure event type = %q, want %q", gotEvent.Type(), "greet")
	}
	if gotResp.StatusCode == "" || !strings.Contains(err.Error(), gotResp.StatusCode) {
		t.Errorf("error %q should mention the failing status %q", err.Error(), gotResp.StatusCode)
	}
}

func TestDispatchCloudEvent_NilOnFailureIsSafe(t *testing.T) {
	builder := newTestBuilder(t, wire.ReservedNames{})
	e := newGreetEvent(t, "") // fails, no hook installed

	if err := dispatchCloudEvent(context.Background(), e, builder, builder.ReservedNames, nil); err == nil {
		t.Fatal("dispatchCloudEvent() error = nil, want non-nil for an unsuccessful result")
	}
}

func TestDispatchCloudEvent_EmptyTypeFallsBackToReservedTopicKey(t *testing.T) {
	// A producer that does not set the CloudEvent type carries the topic in an extension
	// attribute under the (overridden) reserved topic key instead.
	builder := newTestBuilder(t, wire.ReservedNames{TopicKey: "benzenetopic"})
	e := event.New()
	e.SetID("evt-2")
	e.SetSource("/producer")
	e.SetExtension("benzenetopic", "greet")
	if err := e.SetData(cloudevents.ApplicationJSON, greetRequest{Name: "Fallback"}); err != nil {
		t.Fatalf("SetData() error = %v", err)
	}

	if err := dispatchCloudEvent(context.Background(), e, builder, builder.ReservedNames, nil); err != nil {
		t.Fatalf("dispatchCloudEvent() error = %v, want nil (topic resolved from the reserved key)", err)
	}
}

func TestDispatchCloudEvent_EmptyTypeAndNoFallbackIsAFailureNotAPanic(t *testing.T) {
	builder := newTestBuilder(t, wire.ReservedNames{})
	e := event.New() // no type, no data, no routing extension
	e.SetID("evt-3")
	e.SetSource("/producer")

	if err := dispatchCloudEvent(context.Background(), e, builder, builder.ReservedNames, nil); err == nil {
		t.Fatal("dispatchCloudEvent() error = nil, want non-nil for an unresolvable topic")
	}
}

func TestRequestFromEvent_ResolvesTopicBodyAndHeaders(t *testing.T) {
	e := event.New()
	e.SetID("id-42")
	e.SetSource("/orders")
	e.SetType("orders:create")
	e.SetSubject("order-7")
	e.SetTime(time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	e.SetExtension("tenant", "acme")
	if err := e.SetData(cloudevents.ApplicationJSON, map[string]any{"sku": "abc"}); err != nil {
		t.Fatalf("SetData() error = %v", err)
	}

	req := requestFromEvent(e, wire.ReservedNames{})

	if req.Topic != "orders:create" {
		t.Errorf("Topic = %q, want %q", req.Topic, "orders:create")
	}
	if !strings.Contains(req.Body, `"sku":"abc"`) {
		t.Errorf("Body = %q, want the event data JSON", req.Body)
	}
	wantHeaders := map[string]string{
		"ce-id":              "id-42",
		"ce-source":          "/orders",
		"ce-subject":         "order-7",
		"ce-specversion":     "1.0",
		"ce-datacontenttype": cloudevents.ApplicationJSON,
		"ce-tenant":          "acme",
	}
	for k, v := range wantHeaders {
		if req.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, req.Headers[k], v)
		}
	}
	if req.Headers["ce-time"] == "" {
		t.Error("Headers[ce-time] is empty, want the formatted event time")
	}
}

func TestRequestFromEvent_EmptyDataYieldsEmptyBody(t *testing.T) {
	e := event.New()
	e.SetID("id-1")
	e.SetSource("/s")
	e.SetType("greet")

	req := requestFromEvent(e, wire.ReservedNames{})
	if req.Topic != "greet" {
		t.Errorf("Topic = %q, want %q", req.Topic, "greet")
	}
	if req.Body != "" {
		t.Errorf("Body = %q, want empty for an event with no data", req.Body)
	}
}

func TestStringExtensions(t *testing.T) {
	tests := []struct {
		name       string
		extensions map[string]interface{}
		want       map[string]string
	}{
		{
			name:       "nil extensions yield nil",
			extensions: nil,
			want:       nil,
		},
		{
			name:       "typed values render as canonical strings, names lower-cased",
			extensions: map[string]interface{}{"Traceid": "abc", "count": int32(3), "ok": true},
			want:       map[string]string{"traceid": "abc", "count": "3", "ok": "true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringExtensions(tt.extensions)
			if len(got) != len(tt.want) {
				t.Fatalf("stringExtensions() = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("stringExtensions()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestStringExtensions_NonCloudEventsTypeUsesFmtFallback(t *testing.T) {
	// A value that is not one of the CloudEvents attribute types (bool/int32/string/[]byte/URI/
	// Timestamp) - here a nested map - must render via the fmt fallback, not be dropped.
	got := stringExtensions(map[string]interface{}{"weird": map[string]int{"a": 1}})
	if got["weird"] == "" {
		t.Errorf("stringExtensions() dropped a non-CloudEvents-typed value: %v", got)
	}
}

// TestRegisterHTTP_ServesHTTPBindingHandler proves the handler RegisterHTTP wires up is the
// native httpbinding.Handler: the same route table, dispatch, and status mapping. RegisterHTTP
// itself (the functions.HTTP call) is exercised once in TestRegistration_DoesNotPanic; here we
// hit the handler directly, the observable behavior a deployed function would serve.
func TestRegisterHTTP_ServesHTTPBindingHandler(t *testing.T) {
	builder := newTestBuilder(t, wire.ReservedNames{})
	handler := httpbinding.Handler(builder, []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/greet", strings.NewReader(`{"name":"HTTP"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Hello, HTTP!") {
		t.Errorf("body = %q, want the greeting", rec.Body.String())
	}
}

// TestRegistration_DoesNotPanic exercises the two framework-registration wrappers themselves.
// functions.HTTP / functions.CloudEvent register into the framework's process-global registry
// (they do not start a server), so this is the one place the live-only glue runs; each name is
// unique because a duplicate registration log.Fatalf-exits the process.
func TestCloudEventHandler_BindsConfigToDispatch(t *testing.T) {
	builder := newTestBuilder(t, wire.ReservedNames{})
	handler := cloudEventHandler(builder, cloudEventConfig{names: builder.ReservedNames})

	if err := handler(context.Background(), newGreetEvent(t, "Bound")); err != nil {
		t.Fatalf("handler() error = %v, want nil", err)
	}
	if err := handler(context.Background(), newGreetEvent(t, "")); err == nil {
		t.Fatal("handler() error = nil, want non-nil for an unsuccessful dispatch")
	}
}

func TestRegistration_DoesNotPanic(t *testing.T) {
	builder := newTestBuilder(t, wire.ReservedNames{})
	RegisterHTTP("gcpfunctions-test-http", builder, []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}})
	RegisterCloudEvent("gcpfunctions-test-ce", builder)
	RegisterCloudEvent("gcpfunctions-test-ce-opts", builder,
		WithReservedNames(wire.ReservedNames{TopicKey: "benzenetopic"}),
		WithOnFailure(func(context.Context, cloudevents.Event, wire.Response) {}),
	)
}
