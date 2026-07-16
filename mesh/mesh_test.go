package mesh

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

type echoRequest struct {
	Text string `json:"text"`
}

type echoResponse struct {
	Text string `json:"text"`
}

func echoHandler(_ context.Context, req echoRequest) benzene.Result[echoResponse] {
	return benzene.Ok(echoResponse{Text: req.Text})
}

func newTestRegistry(t *testing.T, topics ...benzene.Topic) *benzene.Registry {
	t.Helper()
	registry := benzene.NewRegistry()
	for _, topic := range topics {
		if err := benzene.Register(registry, topic, benzene.Handler[echoRequest, echoResponse](echoHandler)); err != nil {
			t.Fatalf("Register(%v) error = %v", topic, err)
		}
	}
	return registry
}

func TestDescribe(t *testing.T) {
	info := ServiceInfo{
		Service:        "orders",
		ServiceVersion: "1.2.3",
		InstanceID:     "orders-1",
		Binding:        "http",
		Placement:      Placement{Cloud: "aws", Region: "eu-west-1"},
	}

	t.Run("derives the topic list from the registry, sorted", func(t *testing.T) {
		registry := newTestRegistry(t,
			benzene.NewTopic("order:update"),
			benzene.NewTopic("order:create").WithVersion("v2"),
			benzene.NewTopic("order:create"),
		)

		desc := Describe(registry, info)

		want := []TopicDescriptor{
			{ID: "order:create"},
			{ID: "order:create", Version: "v2"},
			{ID: "order:update"},
		}
		if len(desc.Topics) != len(want) {
			t.Fatalf("Topics = %v, want %v", desc.Topics, want)
		}
		for i := range want {
			if desc.Topics[i].ID != want[i].ID || desc.Topics[i].Version != want[i].Version {
				t.Errorf("Topics[%d] = %v, want %v", i, desc.Topics[i], want[i])
			}
			if desc.Topics[i].RequestSchema == nil || desc.Topics[i].ResponseSchema == nil {
				t.Errorf("Topics[%d] schemas = %v/%v, want both derived", i, desc.Topics[i].RequestSchema, desc.Topics[i].ResponseSchema)
			}
		}
		if len(desc.Degraded) != 0 {
			t.Errorf("Degraded = %v, want empty", desc.Degraded)
		}
	})

	t.Run("derives request and response schemas from the handler types", func(t *testing.T) {
		registry := newTestRegistry(t, benzene.NewTopic("order:create"))

		desc := Describe(registry, info)

		schema := desc.Topics[0].RequestSchema
		if schema["type"] != "object" {
			t.Fatalf(`RequestSchema["type"] = %v, want "object": %v`, schema["type"], schema)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("RequestSchema has no properties: %v", schema)
		}
		text, ok := properties["text"].(map[string]any)
		if !ok || text["type"] != "string" {
			t.Errorf(`properties["text"] = %v, want {"type":"string"} (echoRequest.Text has json:"text")`, properties["text"])
		}
	})

	t.Run("copies identity and stamps the runtime", func(t *testing.T) {
		desc := Describe(newTestRegistry(t), info)

		if desc.Service != "orders" || desc.ServiceVersion != "1.2.3" || desc.InstanceID != "orders-1" || desc.Binding != "http" {
			t.Errorf("identity fields = %+v, want copied from %+v", desc, info)
		}
		if desc.Runtime != "go" {
			t.Errorf("Runtime = %q, want %q", desc.Runtime, "go")
		}
	})

	t.Run("explicit placement overrides detection", func(t *testing.T) {
		desc := Describe(newTestRegistry(t), info)

		if desc.Placement != (Placement{Cloud: "aws", Region: "eu-west-1"}) {
			t.Errorf("Placement = %+v, want the explicit override", desc.Placement)
		}
	})

	t.Run("empty placement falls back to detection", func(t *testing.T) {
		for _, v := range []string{"AWS_LAMBDA_FUNCTION_NAME", "FUNCTIONS_CUSTOMHANDLER_PORT", "K_SERVICE"} {
			t.Setenv(v, "")
		}

		desc := Describe(newTestRegistry(t), ServiceInfo{Service: "orders"})

		if desc.Placement.Cloud != "self-hosted" {
			t.Errorf("Placement.Cloud = %q, want %q", desc.Placement.Cloud, "self-hosted")
		}
	})

	t.Run("nil registry degrades the feed, not the descriptor", func(t *testing.T) {
		desc := Describe(nil, info)

		if desc.Topics == nil || len(desc.Topics) != 0 {
			t.Errorf("Topics = %v, want empty non-nil", desc.Topics)
		}
		if len(desc.Degraded) != 1 || desc.Degraded[0] != FeedRegistry {
			t.Errorf("Degraded = %v, want [%q]", desc.Degraded, FeedRegistry)
		}
		if desc.Service != "orders" || desc.Runtime != "go" {
			t.Errorf("identity should survive a missing registry feed, got %+v", desc)
		}
	})
}

func TestDescriptor_WireFieldNamesAreCamelCase(t *testing.T) {
	desc := Describe(nil, ServiceInfo{
		Service:        "orders",
		ServiceVersion: "1.0.0",
		InstanceID:     "orders-1",
		Binding:        "http",
		Placement:      Placement{Cloud: "aws", Region: "eu-west-1"},
	})

	data, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	for _, key := range []string{"service", "serviceVersion", "instanceId", "runtime", "binding", "placement", "topics", "descriptorHash", "degraded"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("marshaled descriptor is missing key %q: %s", key, data)
		}
	}
	placement, ok := raw["placement"].(map[string]any)
	if !ok {
		t.Fatalf("placement is not an object: %s", data)
	}
	for _, key := range []string{"cloud", "region"} {
		if _, present := placement[key]; !present {
			t.Errorf("marshaled placement is missing key %q: %s", key, data)
		}
	}
}

func TestMiddleware(t *testing.T) {
	descriptor := Describe(newTestRegistry(t, benzene.NewTopic("order:create")), ServiceInfo{
		Service:   "orders",
		Placement: Placement{Cloud: "aws"},
	})

	tests := []struct {
		name      string
		topic     benzene.Topic
		aliases   []string
		intercept bool
	}{
		{name: "intercepts the reserved topic", topic: benzene.NewTopic("mesh"), intercept: true},
		{name: "intercepts by id regardless of version", topic: benzene.NewTopic("mesh").WithVersion("v9"), intercept: true},
		{name: "intercepts a configured alias", topic: benzene.NewTopic("_mesh"), aliases: []string{"_mesh"}, intercept: true},
		{name: "passes any other topic through", topic: benzene.NewTopic("order:create"), intercept: false},
		{name: "does not intercept an unconfigured alias", topic: benzene.NewTopic("_mesh"), intercept: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextRan := false
			pipeline := benzene.NewPipeline(
				Middleware(descriptor, tt.aliases...),
				func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
					nextRan = true
					return next(ctx)
				},
			)
			ic := benzene.NewInvocationContext(tt.topic, nil, nil, nil)

			if err := pipeline.Run(context.Background(), ic); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if nextRan == tt.intercept {
				t.Errorf("next ran = %v, want %v", nextRan, !tt.intercept)
			}
			if !tt.intercept {
				if ic.Result != nil {
					t.Errorf("Result = %v, want nil for a passed-through topic", ic.Result)
				}
				return
			}
			if ic.Result == nil {
				t.Fatal("Result = nil, want the descriptor")
			}
			if ic.Result.ResultStatus() != benzene.StatusOk {
				t.Errorf("ResultStatus() = %q, want %q", ic.Result.ResultStatus(), benzene.StatusOk)
			}
			got, ok := ic.Result.ResultPayload().(Descriptor)
			if !ok {
				t.Fatalf("ResultPayload() = %T, want Descriptor", ic.Result.ResultPayload())
			}
			if got.Service != descriptor.Service || len(got.Topics) != len(descriptor.Topics) {
				t.Errorf("payload = %+v, want %+v", got, descriptor)
			}
		})
	}
}
