package benzenetest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

// This file is the harness dogfooding its own contract: it boots a real benzene.App from a
// composition root, pushes native events in the front door via the transport Send* helpers, and
// asserts on the native response AND on egress through a FakeMessageSender - the gold-standard
// shape adopters are asked to copy. It doubles as the internal feature-test for benzenetest.

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

type orderRequest struct {
	ID string `json:"id"`
}

// greetHandler is a plain request/response handler - the ingress side.
func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

// publishHandler resolves the outbound Sender from its scope (the swappable external edge) and
// republishes the order on the "order:created" topic - the egress side.
func publishHandler(ctx context.Context, req orderRequest) benzene.Result[orderRequest] {
	scope, ok := benzene.ScopeFromContext(ctx)
	if !ok {
		return benzene.UnexpectedError[orderRequest]("no DI scope on context")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return benzene.UnexpectedError[orderRequest]("marshal: " + err.Error())
	}
	result := client.SenderFromScope(scope).Send(ctx, benzene.NewTopic("order:created"), nil, body)
	if !result.Status.IsSuccess() {
		return benzene.Result[orderRequest]{Status: result.Status, Errors: result.Errors}
	}
	return benzene.Accepted(req)
}

// newTestApp is the composition root under test: it registers the outbound Sender in the
// container (so a test can swap it) and both handlers, then builds the pipeline in Configure.
func newTestApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		ConfigureServices: func(registry *benzene.Registry, container *benzene.Container, _ struct{}) {
			client.RegisterSender(container, client.SenderFunc(func(context.Context, benzene.Topic, map[string]string, []byte) benzene.Result[json.RawMessage] {
				return benzene.Accepted[json.RawMessage](nil)
			}))
			if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
				panic(err)
			}
			if err := benzene.Register(registry, benzene.NewTopic("order:create"), benzene.Handler[orderRequest, orderRequest](publishHandler)); err != nil {
				panic(err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

func testRoutes() []httpbinding.Route {
	return []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
		{Method: http.MethodPost, Path: "/orders/publish", Topic: benzene.NewTopic("order:create")},
	}
}

func TestSendAPIGateway_ReturnsNativeResponse(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), benzenetest.WithRoutes(testRoutes()...))

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestSendAPIGateway_MissingNameIsBadRequest(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), benzenetest.WithRoutes(testRoutes()...))

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/greet", greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", resp.StatusCode)
	}
}

// SendHTTP is the native-HTTP front door: parallel in name, argument order, and return shape with
// SendAPIGateway (only the Send* call names the transport), driving the real httpbinding.Handler.
func TestSendHTTP_ReturnsNativeResponse(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), benzenetest.WithRoutes(testRoutes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: "World"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestSendHTTP_MissingNameIsBadRequest(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), benzenetest.WithRoutes(testRoutes()...))

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/greet", greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", resp.StatusCode)
	}
}

// The same ingress -> handler -> egress flow as the API Gateway flagship, over the native-HTTP
// front door: the SAME app, the SAME override, the SAME egress assertion - only SendHTTP names the
// transport.
func TestSendHTTP_PublishesOnEgressTopic(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender()
	host := benzenetest.NewHost(newTestApp(),
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			client.RegisterSender(b.Container, fake)
		}),
		benzenetest.WithRoutes(testRoutes()...),
	)

	resp := benzenetest.SendHTTP(t, host, http.MethodPost, "/orders/publish", orderRequest{ID: "order-1"}, nil)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want 202; body = %s", resp.StatusCode, resp.Body)
	}
	if got := fake.LastTopic(); got != benzene.NewTopic("order:created") {
		t.Errorf("LastTopic = %v, want order:created", got)
	}
	var published orderRequest
	fake.DecodeLastMessage(t, &published)
	if published.ID != "order-1" {
		t.Errorf("published ID = %q, want %q", published.ID, "order-1")
	}
}

// The flagship: ingress (API Gateway) -> handler -> egress (faked Sender), asserting on both the
// native response and what the service published. Only WithServices names the fake; the rest is
// identical to any other test.
func TestSendAPIGateway_PublishesOnEgressTopic(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender()
	host := benzenetest.NewHost(newTestApp(),
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			client.RegisterSender(b.Container, fake)
		}),
		benzenetest.WithRoutes(testRoutes()...),
	)

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/orders/publish", orderRequest{ID: "order-1"}, nil)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want 202; body = %s", resp.StatusCode, resp.Body)
	}
	if got := fake.LastTopic(); got != benzene.NewTopic("order:created") {
		t.Errorf("LastTopic = %v, want order:created", got)
	}
	var published orderRequest
	fake.DecodeLastMessage(t, &published)
	if published.ID != "order-1" {
		t.Errorf("published ID = %q, want %q", published.ID, "order-1")
	}
	if fake.Calls() != 1 {
		t.Errorf("Calls = %d, want 1", fake.Calls())
	}
}

// The consistency law in one test: the SAME app, the SAME override, the SAME egress assertion -
// only the Send* call names the transport.
func TestSendPubSub_SameEgressDifferentTransport(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender()
	host := benzenetest.NewHost(newTestApp(),
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			client.RegisterSender(b.Container, fake)
		}),
	)

	resp := benzenetest.SendPubSub(t, host, benzene.NewTopic("order:create"), orderRequest{ID: "order-2"}, nil)

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("StatusCode = %d, want 204 (ack); body = %s", resp.StatusCode, resp.Body)
	}
	if got := fake.LastTopic(); got != benzene.NewTopic("order:created") {
		t.Errorf("LastTopic = %v, want order:created", got)
	}
}

func TestSendPubSub_FailedMessageIsNacked(t *testing.T) {
	host := benzenetest.NewHost(newTestApp())

	resp := benzenetest.SendPubSub(t, host, benzene.NewTopic("greet"), greetRequest{Name: ""}, nil)

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500 (nack)", resp.StatusCode)
	}
}

func TestSendEnvelope_RoundTrip(t *testing.T) {
	host := benzenetest.NewHost(newTestApp())

	resp := benzenetest.SendEnvelope(t, host, benzene.NewTopic("greet"), greetRequest{Name: "Envelope"}, nil)

	if resp.StatusCode != string(benzene.StatusOk) {
		t.Fatalf("StatusCode = %q, want %q; body = %s", resp.StatusCode, benzene.StatusOk, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if greeting.Greeting != "Hello, Envelope!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, Envelope!")
	}
}

func TestSendAzureHTTP_ReturnsNativeResponse(t *testing.T) {
	routes := []httpbinding.Route{{Method: http.MethodPost, Path: "/Greet", Topic: benzene.NewTopic("greet")}}
	host := benzenetest.NewHost(newTestApp(), benzenetest.WithRoutes(routes...))

	resp := benzenetest.SendAzureHTTP(t, host, http.MethodPost, "/Greet", greetRequest{Name: "Azure"}, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200; body = %s", resp.StatusCode, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if greeting.Greeting != "Hello, Azure!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, Azure!")
	}
}

// withOrderBatchHandler registers a fan-in handler for the "orders:changed" topic whose request is
// the whole change-feed batch (a slice) - the shape the Cosmos DB Change Feed trigger delivers. It
// fails the batch if any document has an empty id, so a test can exercise both the checkpoint (200)
// and the redeliver-whole-batch (500) paths.
func withOrderBatchHandler() benzenetest.Option {
	return benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
		if err := benzene.Register(b.Registry, benzene.NewTopic("orders:changed"), benzene.Handler[[]orderRequest, struct{}](
			func(_ context.Context, batch []orderRequest) benzene.Result[struct{}] {
				for _, o := range batch {
					if o.ID == "" {
						return benzene.BadRequest[struct{}]("order id is required")
					}
				}
				return benzene.Ok(struct{}{})
			})); err != nil {
			panic(err)
		}
	})
}

// orderDoc is a row of an "orders" table, as a DynamoDB stream record delivers it after the
// binding converts the AttributeValue image back to plain JSON.
type orderDoc struct {
	ID     string  `json:"id"`
	Amount float64 `json:"amount"`
}

// withOrderInsertHandler registers a handler for the "orders:INSERT" topic (the {table}:{event}
// shape a DynamoDB stream resolves to), failing a row with no id so a test can drive both the
// success and batch-item-failure paths.
func withOrderInsertHandler() benzenetest.Option {
	return benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
		if err := benzene.Register(b.Registry, benzene.NewTopic("orders:INSERT"), benzene.Handler[orderDoc, struct{}](
			func(_ context.Context, o orderDoc) benzene.Result[struct{}] {
				if o.ID == "" {
					return benzene.BadRequest[struct{}]("order id is required")
				}
				return benzene.Ok(struct{}{})
			})); err != nil {
			panic(err)
		}
	})
}

func TestSendDynamoDBStream_SuccessReportsNoFailures(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), withOrderInsertHandler())

	failures := benzenetest.SendDynamoDBStream(t, host, "INSERT", "orders", "seq-1", orderDoc{ID: "o-1", Amount: 9.99})

	if len(failures) != 0 {
		t.Errorf("failures = %v, want none for a valid row", failures)
	}
}

func TestSendDynamoDBStream_FailedRecordIsReportedBySequenceNumber(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), withOrderInsertHandler())

	failures := benzenetest.SendDynamoDBStream(t, host, "INSERT", "orders", "seq-7", orderDoc{ID: "", Amount: 1})

	if len(failures) != 1 || failures[0] != "seq-7" {
		t.Errorf("failures = %v, want [seq-7] (reported for redelivery)", failures)
	}
}

// richDoc exercises every AttributeValue kind the builder encodes: string (S), number (N), bool
// (BOOL), list (L), map (M), and a nil pointer (NULL).
type richDoc struct {
	ID     string            `json:"id"`
	Count  int               `json:"count"`
	Active bool              `json:"active"`
	Tags   []string          `json:"tags"`
	Meta   map[string]string `json:"meta"`
	Note   *string           `json:"note"`
}

func TestSendDynamoDBStream_RichDocumentRoundTripsThroughAttributeValue(t *testing.T) {
	// A document with every value kind must survive the JSON -> AttributeValue -> JSON round trip
	// (the builder encodes it, the binding decodes it) intact when the handler reads it back.
	var got richDoc
	host := benzenetest.NewHost(newTestApp(), benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
		if err := benzene.Register(b.Registry, benzene.NewTopic("orders:INSERT"), benzene.Handler[richDoc, struct{}](
			func(_ context.Context, d richDoc) benzene.Result[struct{}] {
				got = d
				return benzene.Ok(struct{}{})
			})); err != nil {
			panic(err)
		}
	}))

	want := richDoc{ID: "o-1", Count: 3, Active: true, Tags: []string{"a", "b"}, Meta: map[string]string{"k": "v"}}
	failures := benzenetest.SendDynamoDBStream(t, host, "INSERT", "orders", "seq-1", want)

	if len(failures) != 0 {
		t.Fatalf("failures = %v, want none", failures)
	}
	if got.ID != "o-1" || got.Count != 3 || !got.Active || len(got.Tags) != 2 || got.Tags[1] != "b" || got.Meta["k"] != "v" || got.Note != nil {
		t.Errorf("round-tripped doc = %+v, want %+v with nil Note", got, want)
	}
}

func TestSendAzureQueue_SuccessAcksAndNackOnFailure(t *testing.T) {
	host := benzenetest.NewHost(newTestApp())

	ack := benzenetest.SendAzureQueue(t, host, "queueItem", "/GreetQueue", benzene.NewTopic("greet"), greetRequest{Name: "Queue"}, nil)
	if ack.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200 (ack); body = %s", ack.StatusCode, ack.Body)
	}

	nack := benzenetest.SendAzureQueue(t, host, "queueItem", "/GreetQueue", benzene.NewTopic("greet"), greetRequest{Name: ""}, nil)
	if nack.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500 (nack, redeliver)", nack.StatusCode)
	}
}

func TestSendCosmosChangeFeed_BatchIsCheckpointedOnSuccess(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), withOrderBatchHandler())

	resp := benzenetest.SendCosmosChangeFeed(t, host, "documents", "/OrdersChanged", benzene.NewTopic("orders:changed"),
		[]orderRequest{{ID: "order-1"}, {ID: "order-2"}})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200 (checkpoint); body = %s", resp.StatusCode, resp.Body)
	}
}

func TestSendCosmosChangeFeed_FailedBatchIsRedelivered(t *testing.T) {
	host := benzenetest.NewHost(newTestApp(), withOrderBatchHandler())

	resp := benzenetest.SendCosmosChangeFeed(t, host, "documents", "/OrdersChanged", benzene.NewTopic("orders:changed"),
		[]orderRequest{{ID: "order-1"}, {ID: ""}})

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500 (redeliver whole batch)", resp.StatusCode)
	}
}

// WithServices must land before Configure builds the pipeline; a failure result from the faked
// sender must reach the handler's publish-failure branch, proving the override is what the
// handler actually resolves.
func TestWithServices_OverrideReachesHandler(t *testing.T) {
	fake := benzenetest.NewFakeMessageSender().WithResult(benzene.ServiceUnavailable[json.RawMessage]("egress down"))
	host := benzenetest.NewHost(newTestApp(),
		benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
			client.RegisterSender(b.Container, fake)
		}),
		benzenetest.WithRoutes(testRoutes()...),
	)

	resp := benzenetest.SendAPIGateway(t, host, http.MethodPost, "/orders/publish", orderRequest{ID: "order-3"}, nil)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503 (publish failure propagated)", resp.StatusCode)
	}
}
