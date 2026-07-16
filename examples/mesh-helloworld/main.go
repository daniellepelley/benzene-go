// Command mesh-helloworld runs the whole Benzene Mesh story (docs/design/mesh.md and the
// promoted spec, docs/specification/mesh.md in the main repo) in one process: a meshd
// collector and three services demonstrating every mesh feature.
//
//   - greeter and frontdoor are fully meshed: descriptor endpoint (topics + derived
//     schemas + contract hash), registration, heartbeats, and trace push. frontdoor calls
//     greeter over the wire envelope, propagating its trace span, so the collector derives
//     the frontdoor→greet consumer edge from parentage.
//   - legacy-portal is deliberately reduced: it provisions ONLY the trace feed - no
//     descriptor endpoint, no registration, no heartbeats (the "descriptor endpoint
//     withheld" deployment). It still calls greeter and still appears on the view, as
//     reduced feeds: descriptor, health - anonymous-but-live, exactly the degradation
//     rule the design makes normative.
//
// Run it and open http://localhost:8090/ - then generate flows:
//
//	curl -s -X POST localhost:8081/welcome -d '{"name":"Mesh"}'   # fully meshed path
//	curl -s -X POST localhost:8082/relay   -d '{"name":"Mesh"}'   # reduced-service path
//
// Everything on the view is derived from the running services; nothing here declares any
// catalog data. See the README for drilling into descriptors, topics, and traces with
// curl.
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
	legacyPort    = "8082"
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

// newService assembles a meshed Benzene service: the caller's handlers, health-check
// interception, trace push to the collector, native routes, and the envelope endpoint at
// /invoke. provisionDescriptor controls the reserved-mesh-topic endpoint - passing false
// is the "spec endpoint withheld" deployment: the service still traces, it just serves no
// descriptor (and on the view degrades to reduced, never breaks). The mesh wiring is the
// mesh.* lines in the pipeline; everything else is the same as examples/helloworld.
func newService(name, meshdEndpoint string, provisionDescriptor bool, registerHandlers func(*benzene.Registry), routes []httpbinding.Route) *service {
	registry := benzene.NewRegistry()
	registerHandlers(registry)

	info := mesh.ServiceInfo{Service: name, ServiceVersion: "1.0.0", InstanceID: name + "-1", Binding: "http"}
	descriptor := mesh.Describe(registry, info)
	exporter := mesh.NewPushExporter(httpclient.NewClient(meshdEndpoint), mesh.PushExporterOptions{FlushInterval: time.Second})

	checks := []healthcheck.Check{healthcheck.CheckFunc{CheckName: "self", Fn: func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "self"}
	}}}

	middlewares := []benzene.Middleware{mesh.TraceMiddleware(info, exporter)}
	if provisionDescriptor {
		middlewares = append(middlewares, mesh.Middleware(descriptor))
	}
	middlewares = append(middlewares, healthcheck.Middleware(checks), benzene.RouterMiddleware(registry))

	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(middlewares...),
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

	greeter := newService("greeter", meshdEndpoint, true, func(registry *benzene.Registry) {
		if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
			log.Fatalf("register greet: %v", err)
		}
	}, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
		{Method: http.MethodGet, Path: "/health", Topic: benzene.NewTopic("healthcheck")},
	})
	defer greeter.exporter.Close()
	go func() { log.Fatal(http.ListenAndServe(":"+greeterPort, greeter.handler)) }()

	frontdoor := newService("frontdoor", meshdEndpoint, true, func(registry *benzene.Registry) {
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

	// legacy-portal provisions ONLY the trace feed: no descriptor endpoint (false below),
	// and it never announces or heartbeats. It shows up on the view as reduced -
	// "missing feeds: descriptor, health" - and its calls to greeter still produce the
	// legacy-portal→greet consumer edge. This is the degradation rule, live.
	legacy := newService("legacy-portal", meshdEndpoint, false, func(registry *benzene.Registry) {
		greeterClient := httpclient.NewClient("http://localhost:" + greeterPort + "/invoke")
		if err := benzene.Register(registry, benzene.NewTopic("legacy:relay"), welcomeHandler(greeterClient)); err != nil {
			log.Fatalf("register legacy:relay: %v", err)
		}
	}, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/relay", Topic: benzene.NewTopic("legacy:relay")},
	})
	defer legacy.exporter.Close()
	go func() { log.Fatal(http.ListenAndServe(":"+legacyPort, legacy.handler)) }()

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
	log.Printf("meshed flow    curl -s -X POST localhost:%s/welcome -d '{\"name\":\"Mesh\"}'", frontdoorPort)
	log.Printf("reduced flow   curl -s -X POST localhost:%s/relay -d '{\"name\":\"Mesh\"}'", legacyPort)
	log.Printf("descriptor     curl -s -X POST localhost:%s/invoke -d '{\"topic\":\"mesh\",\"headers\":{},\"body\":\"\"}'", greeterPort)
	select {}
}
