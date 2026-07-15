package benzene

import (
	"context"
	"testing"
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
