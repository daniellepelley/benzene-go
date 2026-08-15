package meshapp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	awslambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambdaclient"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
	"github.com/daniellepelley/benzene-go/wire"
)

// -- test topics/types -------------------------------------------------------------------------

const (
	testTopicHTTP        = "test:http"
	testTopicSQS         = "test:sqs"
	testTopicSNS         = "test:sns"
	testTopicEventBridge = "test:eventbridge"
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

// newMultiShapeApp registers one handler per transport shape so a single App can be driven
// through Handler() with each of the four event shapes classify() distinguishes.
func newMultiShapeApp(meshClient *awslambdaclient.Client) *App {
	return New(Config{
		ServiceName: "multi",
		MeshClient:  meshClient,
		Register: func(registry *benzene.Registry) []httpbinding.Route {
			mustRegister(registry, testTopicHTTP)
			mustRegister(registry, testTopicSQS)
			mustRegister(registry, testTopicSNS)
			mustRegister(registry, testTopicEventBridge)
			return []httpbinding.Route{{Method: http.MethodPost, Path: "/echo", Topic: benzene.NewTopic(testTopicHTTP)}}
		},
	})
}

func mustRegister(registry *benzene.Registry, topic string) {
	if err := benzene.Register(registry, benzene.NewTopic(topic), echoHandler()); err != nil {
		panic(err)
	}
}

// -- classify --------------------------------------------------------------------------------

func TestClassify_DistinguishesEventShapes(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		event := benzenetest.NewAPIGatewayEvent(t, http.MethodPost, "/echo", echoRequest{Value: "x"}, nil)
		if got := classify(event); got != shapeHTTP {
			t.Errorf("classify() = %v, want shapeHTTP", got)
		}
	})
	t.Run("sqs", func(t *testing.T) {
		event := benzenetest.NewSQSEvent(t, "msg-1", benzene.NewTopic(testTopicSQS), echoRequest{Value: "x"}, nil)
		if got := classify(event); got != shapeSQS {
			t.Errorf("classify() = %v, want shapeSQS", got)
		}
	})
	t.Run("sns", func(t *testing.T) {
		event := benzenetest.NewSNSEvent(t, "msg-1", benzene.NewTopic(testTopicSNS), echoRequest{Value: "x"}, nil)
		if got := classify(event); got != shapeSNS {
			t.Errorf("classify() = %v, want shapeSNS", got)
		}
	})
	t.Run("eventbridge", func(t *testing.T) {
		event := newEventBridgeEvent(t, testTopicEventBridge, echoRequest{Value: "x"})
		if got := classify(event); got != shapeEventBridge {
			t.Errorf("classify() = %v, want shapeEventBridge", got)
		}
	})
	t.Run("unrecognised", func(t *testing.T) {
		if got := classify(json.RawMessage(`{"nothing":"recognisable"}`)); got != shapeUnknown {
			t.Errorf("classify() = %v, want shapeUnknown", got)
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		if got := classify(json.RawMessage(`not json`)); got != shapeUnknown {
			t.Errorf("classify() = %v, want shapeUnknown", got)
		}
	})
}

// newEventBridgeEvent builds the Lambda EventBridge rule-invoke payload awseventbridge.Handler
// parses: top-level "detail-type" + "detail", no "Records" - the shape that tells it apart from
// SQS/SNS.
func newEventBridgeEvent(t *testing.T, detailType string, payload any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal EventBridge detail: %v", err)
	}
	event := map[string]any{
		"id":          "evt-1",
		"detail-type": detailType,
		"source":      "test",
		"account":     "000000000000",
		"region":      "us-east-1",
		"time":        "2024-01-01T00:00:00Z",
		"detail":      json.RawMessage(body),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal EventBridge event: %v", err)
	}
	return data
}

// -- Handler: dispatch across the four shapes, with mesh reporting disabled -----------------------

func TestApp_Handler_DispatchesEveryEventShape(t *testing.T) {
	app := newMultiShapeApp(nil)
	handler := app.Handler()
	ctx := context.Background()

	t.Run("http", func(t *testing.T) {
		event := benzenetest.NewAPIGatewayEvent(t, http.MethodPost, "/echo", echoRequest{Value: "hi"}, nil)
		raw, err := handler(ctx, event)
		if err != nil {
			t.Fatalf("handler() error = %v", err)
		}
		var resp struct {
			StatusCode int    `json:"statusCode"`
			Body       string `json:"body"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal HTTP response: %v; raw = %s", err, raw)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("statusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
		}
	})

	t.Run("sqs", func(t *testing.T) {
		event := benzenetest.NewSQSEvent(t, "msg-1", benzene.NewTopic(testTopicSQS), echoRequest{Value: "hi"}, nil)
		raw, err := handler(ctx, event)
		if err != nil {
			t.Fatalf("handler() error = %v", err)
		}
		var resp struct {
			BatchItemFailures []struct{ ItemIdentifier string } `json:"batchItemFailures"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal SQS response: %v; raw = %s", err, raw)
		}
		if len(resp.BatchItemFailures) != 0 {
			t.Errorf("BatchItemFailures = %v, want none", resp.BatchItemFailures)
		}
	})

	t.Run("sns", func(t *testing.T) {
		event := benzenetest.NewSNSEvent(t, "msg-1", benzene.NewTopic(testTopicSNS), echoRequest{Value: "hi"}, nil)
		if _, err := handler(ctx, event); err != nil {
			t.Errorf("handler() error = %v, want nil (successful SNS notification)", err)
		}
	})

	t.Run("eventbridge", func(t *testing.T) {
		event := newEventBridgeEvent(t, testTopicEventBridge, echoRequest{Value: "hi"})
		if _, err := handler(ctx, event); err != nil {
			t.Errorf("handler() error = %v, want nil (successful EventBridge event)", err)
		}
	})

	t.Run("unrecognised shape is reported, not panicked", func(t *testing.T) {
		if _, err := handler(ctx, json.RawMessage(`{}`)); err == nil {
			t.Error("handler() error = nil, want an error for an unrecognised event shape")
		}
	})
}

// -- health route ------------------------------------------------------------------------------

func TestNew_AlwaysAddsHealthRoute(t *testing.T) {
	app := New(Config{ServiceName: "svc", Register: func(*benzene.Registry) []httpbinding.Route { return nil }})
	raw, err := app.Handler()(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodGet, httpbinding.HealthPath, nil, nil))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp struct {
		StatusCode int `json:"statusCode"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s statusCode = %d, want 200", httpbinding.HealthPath, resp.StatusCode)
	}
}

func TestNew_NilRegisterIsFine(t *testing.T) {
	app := New(Config{ServiceName: "svc"})
	if len(app.routes) != 1 {
		t.Fatalf("routes = %v, want just the standard health route", app.routes)
	}
}

// -- fleet reporting: a fake Lambda Invoke that forwards straight into a real meshd.Collector ------

// fakeMeshInvoke satisfies awslambdaclient.InvokeAPI by dispatching the invoke payload directly
// into a real meshd.Collector's pipeline - an in-process stand-in for "Lambda invokes the mesh
// function", so Announce/heartbeat/trace push can be proven against genuine collector behaviour
// with no network and no real AWS credentials, the same spirit as
// examples/k8s-mesh-helloworld/cmd/service's chain test using a real meshd.New() over httptest.
type fakeMeshInvoke struct {
	builder *benzene.ApplicationBuilder
}

func (f *fakeMeshInvoke) Invoke(ctx context.Context, params *awslambdasdk.InvokeInput, _ ...func(*awslambdasdk.Options)) (*awslambdasdk.InvokeOutput, error) {
	req, err := wire.UnmarshalRequest(params.Payload)
	if err != nil {
		return nil, err
	}
	resp := envelope.Dispatch(ctx, f.builder.Pipeline, f.builder.Container, req)
	payload, err := wire.MarshalResponse(resp)
	if err != nil {
		return nil, err
	}
	return &awslambdasdk.InvokeOutput{Payload: payload}, nil
}

func newTestCollectorClient() (*meshd.Collector, *awslambdaclient.Client) {
	collector := meshd.New(meshd.Options{})
	client := awslambdaclient.NewClient(&fakeMeshInvoke{builder: collector.Builder()}, "mesh")
	return collector, client
}

func TestApp_AnnounceAndHandler_ReportToARealCollector(t *testing.T) {
	collector, meshClient := newTestCollectorClient()
	app := newMultiShapeApp(meshClient)
	ctx := context.Background()

	if !app.Announce(ctx) {
		t.Fatal("Announce() = false, want true against a reachable collector")
	}

	// Drive one invocation through each transport shape so every hop gets traced.
	handler := app.Handler()
	if _, err := handler(ctx, benzenetest.NewAPIGatewayEvent(t, http.MethodPost, "/echo", echoRequest{Value: "hi"}, nil)); err != nil {
		t.Fatalf("http invocation: %v", err)
	}
	if _, err := handler(ctx, benzenetest.NewSQSEvent(t, "m1", benzene.NewTopic(testTopicSQS), echoRequest{Value: "hi"}, nil)); err != nil {
		t.Fatalf("sqs invocation: %v", err)
	}

	fleetResult, ok := envelope.DispatchTopicResult(ctx, collector.Builder().Pipeline, collector.Builder().Container, benzene.NewTopic(mesh.TopicQueryFleet), nil, "")
	if !ok {
		t.Fatalf("fleet query failed: %+v", fleetResult)
	}
	var fleet meshd.FleetView
	if err := json.Unmarshal([]byte(fleetResult.Body), &fleet); err != nil {
		t.Fatalf("unmarshal fleet: %v", err)
	}

	var multi *meshd.ServiceSummary
	for i := range fleet.Services {
		if fleet.Services[i].Service == "multi" {
			multi = &fleet.Services[i]
		}
	}
	if multi == nil {
		t.Fatalf("fleet is missing the 'multi' service: %+v", fleet.Services)
	}
	if multi.Health != "healthy" {
		t.Errorf("Health = %q, want healthy (Announce + Handler's heartbeat both landed)", multi.Health)
	}
	if multi.Invocations == 0 {
		t.Error("Invocations = 0, want at least the two driven above - Handler must flush its per-invocation trace exporter before returning")
	}
	if len(multi.MissingFeeds) != 0 {
		t.Errorf("MissingFeeds = %v, want none", multi.MissingFeeds)
	}
}

func TestApp_NilMeshClient_HandlerStillWorksAndAnnounceIsANoOp(t *testing.T) {
	app := newMultiShapeApp(nil)

	if !app.Announce(context.Background()) {
		t.Error("Announce() with no MeshClient = false, want true (a no-op success)")
	}

	raw, err := app.Handler()(context.Background(), benzenetest.NewAPIGatewayEvent(t, http.MethodPost, "/echo", echoRequest{Value: "hi"}, nil))
	if err != nil {
		t.Fatalf("handler() error = %v, want nil even with no mesh reporting configured", err)
	}
	var resp struct{ StatusCode int }
	_ = json.Unmarshal(raw, &resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("statusCode = %d, want 200", resp.StatusCode)
	}
}

// failingInvokeAPI always errors, so Announce exhausts its retry budget - proving Announce gives
// up rather than blocking main() forever when the mesh Lambda genuinely can't be reached.
type failingInvokeAPI struct{ calls int }

func (f *failingInvokeAPI) Invoke(context.Context, *awslambdasdk.InvokeInput, ...func(*awslambdasdk.Options)) (*awslambdasdk.InvokeOutput, error) {
	f.calls++
	return nil, context.DeadlineExceeded
}

func TestApp_Announce_GivesUpAfterBoundedRetries(t *testing.T) {
	api := &failingInvokeAPI{}
	meshClient := awslambdaclient.NewClient(api, "unreachable-mesh")
	app := New(Config{ServiceName: "svc", MeshClient: meshClient})

	start := time.Now()
	ok := app.Announce(context.Background())
	elapsed := time.Since(start)

	if ok {
		t.Error("Announce() = true, want false against a permanently failing mesh client")
	}
	if api.calls != 5 {
		t.Errorf("Invoke calls = %d, want exactly 5 (the bounded retry budget)", api.calls)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Announce() took %v, want a bounded, demo-friendly retry window", elapsed)
	}
}

func TestApp_Announce_RespectsContextCancellation(t *testing.T) {
	api := &failingInvokeAPI{}
	meshClient := awslambdaclient.NewClient(api, "unreachable-mesh")
	app := New(Config{ServiceName: "svc", MeshClient: meshClient})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if app.Announce(ctx) {
		t.Error("Announce() = true, want false on an already-cancelled context")
	}
}

// -- descriptor --------------------------------------------------------------------------------

func TestApp_Descriptor_ReflectsRegisteredTopics(t *testing.T) {
	app := New(Config{
		ServiceName: "svc",
		Register: func(registry *benzene.Registry) []httpbinding.Route {
			mustRegister(registry, "some:topic")
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
