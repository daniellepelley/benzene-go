package meshapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
	"github.com/daniellepelley/benzene-go/wire"
)

type echoRequest struct {
	Value string `json:"value"`
}
type echoResponse struct {
	Value string `json:"value"`
}

func echoHandler() benzene.Handler[echoRequest, echoResponse] {
	return func(_ context.Context, req echoRequest) benzene.Result[echoResponse] {
		return benzene.Ok(echoResponse{Value: req.Value})
	}
}

func newTestApp(t *testing.T, meshClient *httpclient.Client) *App {
	t.Helper()
	return New(Config{
		ServiceName: "svc",
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry, outbound *mesh.OutboundRegistry) []httpbinding.Route {
			if err := benzene.Register(registry, benzene.NewTopic("echo"), echoHandler()); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			if err := mesh.RegisterOutbound[echoRequest, any](outbound, benzene.NewTopic("downstream")); err != nil {
				t.Fatalf("RegisterOutbound() error = %v", err)
			}
			return []httpbinding.Route{{Method: http.MethodPost, Path: "/Echo", Topic: benzene.NewTopic("echo")}}
		},
	})
}

// azureInvocationBody builds the custom-handler HTTP trigger invocation payload the Functions
// host sends: Data.req.{Method,Headers,Body}.
func azureInvocationBody(t *testing.T, method, body string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"Data": map[string]any{
			"req": map[string]any{
				"Method":  method,
				"Headers": map[string]string{},
				"Body":    body,
			},
		},
		"Metadata": map[string]any{},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(payload)
}

func decodeInvocationResult(t *testing.T, rec *httptest.ResponseRecorder) httpOutputBinding {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var inv invocationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rec.Body.String())
	}
	res, ok := inv.Outputs["res"]
	if !ok {
		t.Fatalf("Outputs missing \"res\"; body = %s", rec.Body.String())
	}
	return res
}

// -- New / routes ------------------------------------------------------------------------------

func TestNew_AlwaysAddsSpecAndHealthRoutes(t *testing.T) {
	app := New(Config{ServiceName: "svc"})
	if len(app.routes) != 2 {
		t.Fatalf("routes = %+v, want just the standard Spec+Health routes", app.routes)
	}
}

func TestApp_HTTPHandler_ServesSpecHealthAndCustomRoutes(t *testing.T) {
	app := newTestApp(t, nil)
	handler := app.HTTPHandler()

	t.Run("spec", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/Spec", strings.NewReader(azureInvocationBody(t, http.MethodGet, "")))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		res := decodeInvocationResult(t, rec)
		if res.StatusCode != "200" {
			t.Fatalf("GET /Spec statusCode = %s, want 200; body = %s", res.StatusCode, res.Body)
		}
		var desc mesh.Descriptor
		if err := json.Unmarshal([]byte(res.Body), &desc); err != nil {
			t.Fatalf("unmarshal descriptor: %v; body = %s", err, res.Body)
		}
		if desc.Service != "svc" {
			t.Errorf("Descriptor.Service = %q, want svc", desc.Service)
		}
	})

	t.Run("health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/Health", strings.NewReader(azureInvocationBody(t, http.MethodGet, "")))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		res := decodeInvocationResult(t, rec)
		if res.StatusCode != "200" {
			t.Fatalf("GET /Health statusCode = %s, want 200; body = %s", res.StatusCode, res.Body)
		}
	})

	t.Run("custom route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/Echo", strings.NewReader(azureInvocationBody(t, http.MethodPost, `{"value":"hi"}`)))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		res := decodeInvocationResult(t, rec)
		if res.StatusCode != "200" {
			t.Fatalf("POST /Echo statusCode = %s, want 200; body = %s", res.StatusCode, res.Body)
		}
	})
}

// -- EnvelopeHandler -----------------------------------------------------------------------------

func TestApp_EnvelopeHandler_DispatchesByBodyTopic(t *testing.T) {
	app := newTestApp(t, nil)
	handler := app.EnvelopeHandler()

	envelopeBody, err := json.Marshal(wire.Request{Topic: "echo", Headers: map[string]string{}, Body: `{"value":"envelope"}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/Invoke", strings.NewReader(azureInvocationBody(t, http.MethodPost, string(envelopeBody))))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	res := decodeInvocationResult(t, rec)
	if res.StatusCode != "200" {
		t.Fatalf("outer envelope statusCode = %s, want 200; body = %s", res.StatusCode, res.Body)
	}
	wireResp, err := wire.UnmarshalResponse([]byte(res.Body))
	if err != nil {
		t.Fatalf("unmarshal wire.Response: %v; body = %s", err, res.Body)
	}
	if wireResp.StatusCode != string(benzene.StatusOk) {
		t.Errorf("envelope StatusCode = %q, want %q; body = %s", wireResp.StatusCode, benzene.StatusOk, wireResp.Body)
	}
	var echoed echoResponse
	if err := json.Unmarshal([]byte(wireResp.Body), &echoed); err != nil {
		t.Fatalf("unmarshal echoed payload: %v", err)
	}
	if echoed.Value != "envelope" {
		t.Errorf("echoed.Value = %q, want envelope", echoed.Value)
	}
}

func TestApp_EnvelopeHandler_MalformedEnvelopeIsOuterBadRequest(t *testing.T) {
	app := newTestApp(t, nil)
	handler := app.EnvelopeHandler()

	req := httptest.NewRequest(http.MethodPost, "/Invoke", strings.NewReader(azureInvocationBody(t, http.MethodPost, "not an envelope")))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// -- WrapRawHandler ------------------------------------------------------------------------------

func TestWrapRawHandler_CapturesRawHTTPResponse(t *testing.T) {
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html>fleet view</html>"))
	})
	handler := WrapRawHandler(raw)

	req := httptest.NewRequest(http.MethodPost, "/FleetUi", strings.NewReader(azureInvocationBody(t, http.MethodGet, "")))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	res := decodeInvocationResult(t, rec)
	if res.StatusCode != "200" {
		t.Fatalf("statusCode = %s, want 200", res.StatusCode)
	}
	if res.Body != "<html>fleet view</html>" {
		t.Errorf("body = %q, want the raw handler's HTML verbatim", res.Body)
	}
	if res.Headers["content-type"] != "text/html; charset=utf-8" {
		t.Errorf("content-type header = %q, not carried through", res.Headers["content-type"])
	}
}

func TestWrapRawHandler_MalformedHostPayloadIsBadRequest(t *testing.T) {
	handler := WrapRawHandler(http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodPost, "/FleetUi", strings.NewReader("{not valid json"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// -- fleet reporting: a real meshd.Collector behind an httptest.Server, exactly like
// examples/k8s-mesh-helloworld's own chain test ------------------------------------------------

func newTestCollectorServer(t *testing.T) (*meshd.Collector, *httptest.Server, *httpclient.Client) {
	t.Helper()
	collector := meshd.New(meshd.Options{})
	builder := collector.Builder()
	envelopeHandler := newEnvelopeHandlerForCollector(builder)
	server := httptest.NewServer(envelopeHandler)
	t.Cleanup(server.Close)
	client := httpclient.NewClient(server.URL)
	return collector, server, client
}

// newEnvelopeHandlerForCollector mounts a plain httpbinding.EnvelopeHandler (real HTTP, not the
// Azure custom-handler wrapping) - meshapp's own EnvelopeHandler wraps the custom-handler
// invocation contract, which httpclient.Client (a plain wire-envelope-over-HTTP client) does not
// speak; the collector side of this test only needs a real envelope endpoint to push to.
func newEnvelopeHandlerForCollector(builder *benzene.ApplicationBuilder) http.Handler {
	return httpbinding.EnvelopeHandler(builder)
}

func TestApp_AnnounceAndHeartbeat_ReportToARealCollector(t *testing.T) {
	collector, _, meshClient := newTestCollectorServer(t)
	app := newTestApp(t, meshClient)
	ctx := context.Background()

	if !app.Announce(ctx) {
		t.Fatal("Announce() = false, want true against a reachable collector")
	}
	app.heartbeat(ctx)

	fleetResult, ok := envelope.DispatchTopicResult(ctx, collector.Builder().Pipeline, collector.Builder().Container, benzene.NewTopic(mesh.TopicQueryFleet), nil, "")
	if !ok {
		t.Fatalf("fleet query failed: %+v", fleetResult)
	}
	var fleet meshd.FleetView
	if err := json.Unmarshal([]byte(fleetResult.Body), &fleet); err != nil {
		t.Fatalf("unmarshal fleet: %v", err)
	}

	var svc *meshd.ServiceSummary
	for i := range fleet.Services {
		if fleet.Services[i].Service == "svc" {
			svc = &fleet.Services[i]
		}
	}
	if svc == nil {
		t.Fatalf("fleet is missing the 'svc' service: %+v", fleet.Services)
	}
	if svc.Health != "healthy" {
		t.Errorf("Health = %q, want healthy (Announce + heartbeat both landed)", svc.Health)
	}
}

func TestApp_NilMeshClient_AnnounceIsANoOpAndHeartbeatDoesNothing(t *testing.T) {
	app := newTestApp(t, nil)

	if !app.Announce(context.Background()) {
		t.Error("Announce() with no MeshClient = false, want true (a no-op success)")
	}
	app.heartbeat(context.Background()) // must not panic
}

func TestApp_RunHeartbeatLoop_NilMeshClientReturnsImmediately(t *testing.T) {
	app := newTestApp(t, nil)
	done := make(chan struct{})
	go func() {
		app.RunHeartbeatLoop(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunHeartbeatLoop with no MeshClient did not return promptly")
	}
}

func TestApp_RunHeartbeatLoop_StopsOnContextCancellation(t *testing.T) {
	_, _, meshClient := newTestCollectorServer(t)
	app := newTestApp(t, meshClient)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		app.RunHeartbeatLoop(ctx)
		close(done)
	}()

	// Give the loop a moment to announce + heartbeat once, then cancel and confirm it exits.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunHeartbeatLoop did not stop after context cancellation")
	}
}

func TestApp_Close_IsSafeWithAndWithoutAMeshClient(t *testing.T) {
	newTestApp(t, nil).Close() // must not panic with nil exporters

	_, _, meshClient := newTestCollectorServer(t)
	newTestApp(t, meshClient).Close()
}

// -- descriptor --------------------------------------------------------------------------------

func TestApp_Descriptor_ReflectsRegisteredTopics(t *testing.T) {
	app := New(Config{
		ServiceName: "svc",
		Register: func(registry *benzene.Registry, _ *mesh.OutboundRegistry) []httpbinding.Route {
			if err := benzene.Register(registry, benzene.NewTopic("some:topic"), echoHandler()); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			return nil
		},
	})
	desc := app.Descriptor()
	if desc.Service != "svc" || desc.Runtime != "go" {
		t.Errorf("Descriptor = %+v, want Service=svc Runtime=go", desc)
	}
	found := false
	for _, topic := range desc.Topics {
		if topic.ID == "some:topic" {
			found = true
		}
	}
	if !found {
		t.Errorf("Descriptor.Topics = %+v, want some:topic present", desc.Topics)
	}
}

// TestApp_Descriptor_ReflectsOutboundRegistrations is the send-side counterpart of the test
// above: what Config.Register declares on the OutboundRegistry must reach Descriptor.Consumes,
// since that list - not observed trace parentage - is the sole source of this service's consumer
// edges on the mesh's topic catalog (mesh.md §4).
func TestApp_Descriptor_ReflectsOutboundRegistrations(t *testing.T) {
	desc := newTestApp(t, nil).Descriptor()

	if len(desc.Consumes) != 1 || desc.Consumes[0].ID != "downstream" {
		t.Fatalf("Descriptor.Consumes = %+v, want exactly [downstream]", desc.Consumes)
	}
	// TRes = any derives the unconstrained {} responseSchema mesh.md §2.3 specifies for a
	// sender with no expected response type - present, not omitted.
	if got := desc.Consumes[0].ResponseSchema; got == nil || len(got) != 0 {
		t.Errorf("downstream ResponseSchema = %v, want a present, empty ({}) schema", got)
	}
	if len(desc.Degraded) != 0 {
		t.Errorf("Degraded = %v, want empty (both feeds are wired)", desc.Degraded)
	}
}

// TestApp_Descriptor_NoOutboundRegistrationsIsEmptyNotDegraded pins the distinction a pure event
// consumer (inventory/notifications/analytics) depends on: declaring nothing leaves Consumes
// empty, but must NOT mark the outbound feed Degraded - "sends nothing" and "send side not wired
// up" are different claims, and New always passes a real OutboundRegistry so only the former can
// be reported.
func TestApp_Descriptor_NoOutboundRegistrationsIsEmptyNotDegraded(t *testing.T) {
	app := New(Config{
		ServiceName: "svc",
		Register: func(registry *benzene.Registry, _ *mesh.OutboundRegistry) []httpbinding.Route {
			if err := benzene.Register(registry, benzene.NewTopic("some:topic"), echoHandler()); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			return nil
		},
	})
	desc := app.Descriptor()

	if len(desc.Consumes) != 0 {
		t.Errorf("Descriptor.Consumes = %+v, want empty", desc.Consumes)
	}
	for _, feed := range desc.Degraded {
		if feed == mesh.FeedOutboundRegistry {
			t.Errorf("Degraded = %v, want no %q entry: declaring nothing is not a missing feed", desc.Degraded, mesh.FeedOutboundRegistry)
		}
	}
}

func TestApp_Builder_ExposesTheSharedBuilder(t *testing.T) {
	app := newTestApp(t, nil)
	if app.Builder() == nil {
		t.Fatal("Builder() = nil")
	}
}
