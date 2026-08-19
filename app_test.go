package benzene

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/daniellepelley/benzene-go/wire"
)

type testConfig struct {
	Greeting string
}

func TestApp_Run_ExecutesPhasesInOrder(t *testing.T) {
	var order []string

	app := App[testConfig]{
		GetConfiguration: func() testConfig {
			order = append(order, "GetConfiguration")
			return testConfig{Greeting: "hi"}
		},
		ConfigureServices: func(registry *Registry, container *Container, config testConfig) {
			order = append(order, "ConfigureServices")
			if config.Greeting != "hi" {
				t.Errorf("ConfigureServices saw config.Greeting = %q, want %q", config.Greeting, "hi")
			}
		},
		Configure: func(builder *ApplicationBuilder, config testConfig) {
			order = append(order, "Configure")
			if config.Greeting != "hi" {
				t.Errorf("Configure saw config.Greeting = %q, want %q", config.Greeting, "hi")
			}
		},
	}

	app.Run()

	want := []string{"GetConfiguration", "ConfigureServices", "Configure"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, order[i], want[i])
		}
	}
}

func TestApp_Run_ConfigureServicesAndConfigureAreOptional(t *testing.T) {
	app := App[testConfig]{
		GetConfiguration: func() testConfig { return testConfig{} },
	}

	builder := app.Run()
	if builder == nil {
		t.Fatal("Run() should still return a non-nil ApplicationBuilder when ConfigureServices/Configure are nil")
	}
	if builder.Registry == nil || builder.Container == nil {
		t.Error("Run() should still build a Registry/Container even with no ConfigureServices")
	}
}

func TestApp_Run_NilGetConfigurationYieldsZeroConfig(t *testing.T) {
	// All three phases are optional; a nil GetConfiguration must not panic - it yields the zero
	// value of TConfig, which ConfigureServices/Configure then see.
	var seen testConfig
	app := App[testConfig]{
		ConfigureServices: func(_ *Registry, _ *Container, config testConfig) { seen = config },
	}

	builder := app.Run()
	if builder == nil {
		t.Fatal("Run() with a nil GetConfiguration should still return a builder, not panic")
	}
	if seen != (testConfig{}) {
		t.Errorf("ConfigureServices saw %+v, want the zero testConfig", seen)
	}
}

func TestApp_Run_RegistrySurvivesFromConfigureServicesToConfigure(t *testing.T) {
	topic := NewTopic("hello:world")

	app := App[testConfig]{
		GetConfiguration: func() testConfig { return testConfig{} },
		ConfigureServices: func(registry *Registry, container *Container, config testConfig) {
			if err := Register(registry, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
		},
		Configure: func(builder *ApplicationBuilder, config testConfig) {
			if !builder.Registry.Has(topic) {
				t.Error("the handler registered in ConfigureServices should be visible in Configure via the same Registry")
			}
			builder.UsePipeline(NewPipeline(RouterMiddleware(builder.Registry)))
		},
	}

	builder := app.Run()
	if builder.Pipeline == nil {
		t.Fatal("UsePipeline in Configure should have set builder.Pipeline")
	}

	ic := NewInvocationContext(topic, nil, helloRequest{Name: "World"}, nil)
	if err := builder.Pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Pipeline.Run() error = %v", err)
	}
	if ic.Result.ResultStatus() != StatusOk {
		t.Errorf("ResultStatus() = %q, want %q", ic.Result.ResultStatus(), StatusOk)
	}
}

func TestApplicationBuilder_UsePipeline_ReturnsBuilderForChaining(t *testing.T) {
	b := &ApplicationBuilder{Registry: NewRegistry(), Container: NewContainer()}
	pipeline := NewPipeline()
	returned := b.UsePipeline(pipeline)

	if returned != b {
		t.Error("UsePipeline should return the same builder instance for chaining")
	}
	if b.Pipeline != pipeline {
		t.Error("UsePipeline should set Pipeline on the builder")
	}
}

func TestApplicationBuilder_UseReservedNames_SetsAndChains(t *testing.T) {
	b := &ApplicationBuilder{Registry: NewRegistry(), Container: NewContainer()}
	names := wire.ReservedNames{TopicKey: "x-my-topic"}
	returned := b.UseReservedNames(names)

	if returned != b {
		t.Error("UseReservedNames should return the same builder instance for chaining")
	}
	if b.ReservedNames.Topic() != "x-my-topic" {
		t.Errorf("ReservedNames.Topic() = %q, want %q", b.ReservedNames.Topic(), "x-my-topic")
	}
	// The zero value on a fresh builder yields the default key, so a binding that reads it
	// without any override still resolves the standard "topic".
	fresh := &ApplicationBuilder{}
	if fresh.ReservedNames.Topic() != wire.DefaultTopicKey {
		t.Errorf("fresh builder Topic() = %q, want the default %q", fresh.ReservedNames.Topic(), wire.DefaultTopicKey)
	}
}

func TestApp_Run_InstallsTheDefaultPipelineWhenConfigureSetsNone(t *testing.T) {
	t.Run("with no Configure phase at all", func(t *testing.T) {
		app := App[testConfig]{
			ConfigureServices: func(registry *Registry, _ *Container, _ testConfig) {
				MustRegister(registry, NewTopic("greet"), func(_ context.Context, req appGreetRequest) Result[appGreetResponse] {
					return Ok(appGreetResponse{Greeting: "Hello, " + req.Name})
				})
			},
		}

		builder := app.Run()
		if builder.Pipeline == nil {
			t.Fatal("Run() left Pipeline nil; the default pipeline must be installed at start-up, not discovered on the message path")
		}

		ic := NewInvocationContext(NewTopic("greet"), nil, json.RawMessage(`{"name":"World"}`), builder.Container.NewScope())
		if err := builder.Pipeline.Run(context.Background(), ic); err != nil {
			t.Fatalf("Pipeline.Run() error = %v", err)
		}
		payload, ok := ic.Result.ResultPayload().(appGreetResponse)
		if !ok || payload.Greeting != "Hello, World" {
			t.Errorf("default pipeline produced %#v, want it to route to the registered handler", ic.Result.ResultPayload())
		}
	})

	t.Run("with a Configure phase that does other work but never calls UsePipeline", func(t *testing.T) {
		app := App[testConfig]{
			ConfigureServices: func(registry *Registry, _ *Container, _ testConfig) {
				MustRegister(registry, NewTopic("greet"), func(_ context.Context, req appGreetRequest) Result[appGreetResponse] {
					return Ok(appGreetResponse{Greeting: req.Name})
				})
			},
			Configure: func(builder *ApplicationBuilder, _ testConfig) {
				builder.UseReservedNames(wire.ReservedNames{TopicKey: "x-topic"})
			},
		}

		builder := app.Run()
		if builder.Pipeline == nil {
			t.Fatal("Run() left Pipeline nil after a Configure that set no pipeline")
		}
	})
}

func TestApp_Run_ConfigurePipelineWinsOverTheDefault(t *testing.T) {
	var ran []string
	marker := func(name string) Middleware {
		return func(ctx context.Context, ic *InvocationContext, next func(context.Context) error) error {
			ran = append(ran, name)
			return next(ctx)
		}
	}

	app := App[testConfig]{
		Configure: func(builder *ApplicationBuilder, _ testConfig) {
			builder.UsePipeline(NewPipeline(marker("mine")))
		},
	}

	builder := app.Run()
	if err := builder.Pipeline.Run(context.Background(), NewInvocationContext(NewTopic("x"), nil, nil, builder.Container.NewScope())); err != nil {
		t.Fatalf("Pipeline.Run() error = %v", err)
	}
	if len(ran) != 1 || ran[0] != "mine" {
		t.Errorf("ran = %v, want only the pipeline Configure set - the default must never replace an explicit UsePipeline", ran)
	}
	if ic := builder.Pipeline; ic == nil {
		t.Fatal("Pipeline was replaced with nil")
	}
}

func TestApplicationBuilder_UseDefaultPipeline_ComposesTheExplicitForm(t *testing.T) {
	// The shorthand must be indistinguishable from the explicit form a user would write by
	// hand, so the same message routes identically through both.
	registry := NewRegistry()
	MustRegister(registry, NewTopic("greet"), func(_ context.Context, req appGreetRequest) Result[appGreetResponse] {
		return Ok(appGreetResponse{Greeting: "Hello, " + req.Name})
	})
	container := NewContainer()

	shorthand := (&ApplicationBuilder{Registry: registry, Container: container}).UseDefaultPipeline()
	explicit := (&ApplicationBuilder{Registry: registry, Container: container}).
		UsePipeline(NewPipeline(RouterMiddleware(registry)))

	for name, builder := range map[string]*ApplicationBuilder{"shorthand": shorthand, "explicit": explicit} {
		ic := NewInvocationContext(NewTopic("greet"), nil, json.RawMessage(`{"name":"World"}`), builder.Container.NewScope())
		if err := builder.Pipeline.Run(context.Background(), ic); err != nil {
			t.Fatalf("%s: Pipeline.Run() error = %v", name, err)
		}
		payload, ok := ic.Result.ResultPayload().(appGreetResponse)
		if !ok || payload.Greeting != "Hello, World" {
			t.Errorf("%s: payload = %#v, want the routed handler's response", name, ic.Result.ResultPayload())
		}
	}
}

func TestPipeline_Run_NilPipelineReportsTheFixInsteadOfPanicking(t *testing.T) {
	// A hand-built ApplicationBuilder (not one from App.Run) can still carry no pipeline. A
	// binding must get an error naming the fix, not a nil dereference that crashes the
	// transport on its first message.
	var pipeline *Pipeline

	err := pipeline.Run(context.Background(), NewInvocationContext(NewTopic("greet"), nil, nil, NewContainer().NewScope()))
	if err == nil {
		t.Fatal("nil Pipeline.Run() returned no error")
	}
	for _, want := range []string{"no pipeline configured", "UseDefaultPipeline", "UsePipeline", "App.Run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

type appGreetRequest struct {
	Name string `json:"name"`
}

type appGreetResponse struct {
	Greeting string `json:"greeting"`
}
