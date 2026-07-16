// Command mesh-helloworld runs the whole Benzene Mesh story (docs/design/mesh.md,
// Phases 1-4) in one process: a meshd collector, and two meshed services - greeter, and
// frontdoor, which calls greeter over the wire envelope, propagating its trace span so
// the collector can derive the frontdoor→greet consumer edge from parentage.
//
// Run it and open http://localhost:8090/ - the Mesh View shows both services (health from
// heartbeats, topics from descriptors), the topic catalog with the observed consumer
// edge, and every flow you generate with:
//
//	curl -s -X POST localhost:8081/welcome -d '{"name":"Mesh"}'
//
// Everything on the view is derived from the running services; nothing here declares any
// catalog data. Each mesh feed is optional (see the mesh package doc) - deleting the
// mesh.Middleware line below, for example, demotes a service to anonymous-but-live on the
// view instead of breaking anything.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
)

const (
	meshdPort     = "8090"
	greeterPort   = "8080"
	frontdoorPort = "8081"
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

type welcomeRequest struct {
	Name string `json:"name"`
}

type welcomeResponse struct {
	Message string `json:"message"`
}

// welcomeHandler is the cross-service hop: it calls greeter's "greet" topic over the wire
// envelope, forwarding its own trace span as a traceparent header. That propagation is
// the only mesh-specific line a handler ever writes, and it is what joins the two
// services' trace events into one flow (and derives the consumer edge) on the collector.
func welcomeHandler(greeter *httpclient.Client) benzene.Handler[welcomeRequest, welcomeResponse] {
	return func(ctx context.Context, req welcomeRequest) benzene.Result[welcomeResponse] {
		headers := map[string]string{}
		if span, ok := mesh.SpanFromContext(ctx); ok {
			headers["traceparent"] = span.Traceparent()
		}

		body, err := json.Marshal(greetRequest{Name: req.Name})
		if err != nil {
			return benzene.UnexpectedError[welcomeResponse]("marshal greet request: " + err.Error())
		}
		result := greeter.Send(ctx, benzene.NewTopic("greet"), headers, body)
		if !result.IsSuccessful() {
			return benzene.Result[welcomeResponse]{Status: result.Status, Errors: result.Errors}
		}
		typed, err := httpclient.Unmarshal[greetResponse](result)
		if err != nil || typed.Payload == nil {
			return benzene.UnexpectedError[welcomeResponse]("unexpected greet response")
		}
		return benzene.Ok(welcomeResponse{Message: "frontdoor relays: " + typed.Payload.Greeting})
	}
}

// service is one meshed service, ready to serve and to announce itself.
type service struct {
	handler    http.Handler
	exporter   *mesh.PushExporter // caller owns Close
	descriptor mesh.Descriptor
	meshd      *httpclient.Client
}

// newService assembles a meshed Benzene service: the caller's handlers, health-check and
// mesh descriptor interception, trace push to the collector, native routes, and the
// envelope endpoint at /invoke. The mesh wiring is the three mesh.* lines in the
// pipeline; everything else is the same as examples/helloworld.
func newService(name, meshdEndpoint string, registerHandlers func(*benzene.Registry), routes []httpbinding.Route) *service {
	registry := benzene.NewRegistry()
	registerHandlers(registry)

	info := mesh.ServiceInfo{Service: name, ServiceVersion: "1.0.0", InstanceID: name + "-1", Binding: "http"}
	descriptor := mesh.Describe(registry, info)
	exporter := mesh.NewPushExporter(httpclient.NewClient(meshdEndpoint), mesh.PushExporterOptions{FlushInterval: time.Second})

	checks := []healthcheck.Check{healthcheck.CheckFunc{CheckName: "self", Fn: func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "self"}
	}}}

	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline: benzene.NewPipeline(
			mesh.TraceMiddleware(info, exporter),
			mesh.Middleware(descriptor),
			healthcheck.Middleware(checks),
			benzene.RouterMiddleware(registry),
		),
	}

	mux := http.NewServeMux()
	mux.Handle("/invoke", httpbinding.EnvelopeHandler(builder))
	mux.Handle("/", httpbinding.Handler(builder, routes))
	return &service{
		handler:    mux,
		exporter:   exporter,
		descriptor: descriptor,
		meshd:      httpclient.NewClient(meshdEndpoint),
	}
}

// announce registers the service's descriptor with the collector. Failure is logged and
// otherwise ignored: an unreachable collector reduces the mesh, never the service.
func (s *service) announce(ctx context.Context) {
	body, err := json.Marshal(s.descriptor)
	if err != nil {
		log.Printf("%s: marshal descriptor: %v", s.descriptor.Service, err)
		return
	}
	if result := s.meshd.Send(ctx, benzene.NewTopic(mesh.TopicRegister), nil, body); !result.IsSuccessful() {
		log.Printf("%s: register with meshd: %v %v", s.descriptor.Service, result.Status, result.Errors)
	}
}

// heartbeat sends one health report. In a real service the Health field would come from
// running the same checks the healthcheck middleware serves; a static healthy report
// keeps the example focused.
func (s *service) heartbeat(ctx context.Context) {
	body, err := json.Marshal(mesh.Heartbeat{
		Service:        s.descriptor.Service,
		InstanceID:     s.descriptor.InstanceID,
		DescriptorHash: s.descriptor.DescriptorHash,
		SentAt:         time.Now().UTC(),
		Health:         healthcheck.Response{IsHealthy: true, HealthChecks: map[string]healthcheck.CheckResult{}},
	})
	if err != nil {
		log.Printf("%s: marshal heartbeat: %v", s.descriptor.Service, err)
		return
	}
	if result := s.meshd.Send(ctx, benzene.NewTopic(mesh.TopicHeartbeat), nil, body); !result.IsSuccessful() {
		log.Printf("%s: heartbeat to meshd: %v %v", s.descriptor.Service, result.Status, result.Errors)
	}
}

// newMeshd wires the collector's HTTP entry points: the envelope endpoint its topics are
// served on, and the Mesh View at /.
func newMeshd() http.Handler {
	collector := meshd.New(meshd.Options{})
	mux := http.NewServeMux()
	mux.Handle("/invoke", httpbinding.EnvelopeHandler(collector.Builder()))
	mux.Handle("/", collector.ViewHandler("/invoke"))
	return mux
}

func main() {
	go func() { log.Fatal(http.ListenAndServe(":"+meshdPort, newMeshd())) }()
	meshdEndpoint := "http://localhost:" + meshdPort + "/invoke"

	greeter := newService("greeter", meshdEndpoint, func(registry *benzene.Registry) {
		if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
			log.Fatalf("register greet: %v", err)
		}
	}, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
		{Method: http.MethodGet, Path: "/health", Topic: benzene.NewTopic("healthcheck")},
	})
	defer greeter.exporter.Close()
	go func() { log.Fatal(http.ListenAndServe(":"+greeterPort, greeter.handler)) }()

	frontdoor := newService("frontdoor", meshdEndpoint, func(registry *benzene.Registry) {
		greeterClient := httpclient.NewClient("http://localhost:" + greeterPort + "/invoke")
		if err := benzene.Register(registry, benzene.NewTopic("welcome"), welcomeHandler(greeterClient)); err != nil {
			log.Fatalf("register welcome: %v", err)
		}
	}, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/welcome", Topic: benzene.NewTopic("welcome")},
		{Method: http.MethodGet, Path: "/health", Topic: benzene.NewTopic("healthcheck")},
	})
	defer frontdoor.exporter.Close()
	go func() { log.Fatal(http.ListenAndServe(":"+frontdoorPort, frontdoor.handler)) }()

	ctx := context.Background()
	greeter.announce(ctx)
	frontdoor.announce(ctx)
	greeter.heartbeat(ctx)
	frontdoor.heartbeat(ctx)
	go func() {
		for range time.Tick(10 * time.Second) {
			greeter.heartbeat(ctx)
			frontdoor.heartbeat(ctx)
		}
	}()

	log.Printf("mesh view      http://localhost:%s/", meshdPort)
	log.Printf("try            curl -s -X POST localhost:%s/welcome -d '{\"name\":\"Mesh\"}'", frontdoorPort)
	select {}
}
