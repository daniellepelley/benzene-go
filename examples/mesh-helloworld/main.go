// Command mesh-helloworld runs the whole Benzene Mesh story (work/archive/mesh.md and the
// promoted spec, docs/specification/mesh.md in the main repo) in one process: a meshd
// collector and three services demonstrating every mesh feature.
//
//   - greeter and frontdoor are fully meshed: the Contract Document on benzene:spec (requests,
//     events and sends with derived schemas and both hashes), registration of that same
//     document (including frontdoor's declared send to "greet" - what makes it that topic's
//     declared provider, mesh.md §4), heartbeats, and trace push. frontdoor also propagates its
//     trace span onto its call to greeter, so the collector can additionally show that declared
//     edge as observed (mesh.md §4.2) - but the edge itself, on the topic catalog, comes from
//     frontdoor's registered sends, never from the trace.
//   - legacy-portal is deliberately reduced: it provisions ONLY the trace feed - no
//     self-description, no registration, no heartbeats (the "self-description withheld"
//     deployment). It still calls greeter and still appears on the view, as
//     reduced feeds: descriptor, health - anonymous-but-live, exactly the degradation
//     rule the design makes normative. Because it never registers, it has no declared
//     contract to diverge from: its calls to greeter add up in "greet"'s invocation stats,
//     but they never appear as a consumer edge and are never flagged as drift (mesh.md
//     §4.2) - a live demonstration of "declared, not traffic-derived."
//
// Run it and open http://localhost:8090/ - then generate flows:
//
//	curl -s -X POST localhost:8081/welcome -d '{"name":"Mesh"}'   # fully meshed path
//	curl -s -X POST localhost:8082/relay   -d '{"name":"Mesh"}'   # reduced-service path
//
// The topic catalog (who provides/consumes what) is declared by each service's registered Contract
// Document;
// health, traffic stats, and the observed-activity/drift signals are derived from what the
// running services actually do. See the README for drilling into documents, topics, and
// traces with curl.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/contract"
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
// envelope. Trace propagation - forwarding this invocation's span as a traceparent header, which
// joins the two services' trace events into one flow and lets the collector show the declared
// edge as observed (mesh.md §4.2) - is handled by wrapping greeter in mesh.WithTraceContext at
// construction (see the call sites), so the handler itself writes no mesh-specific line at all.
// The edge on the topic catalog itself comes from the caller's declared send
// (registerOutbound at the newService call site), not from this propagation.
func welcomeHandler(greeter client.Sender) benzene.Handler[welcomeRequest, welcomeResponse] {
	return func(ctx context.Context, req welcomeRequest) benzene.Result[welcomeResponse] {
		body, err := json.Marshal(greetRequest{Name: req.Name})
		if err != nil {
			return benzene.UnexpectedError[welcomeResponse]("marshal greet request: " + err.Error())
		}
		result := greeter.Send(ctx, benzene.NewTopic("greet"), nil, body)
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
	handler       http.Handler
	exporter      *mesh.PushExporter      // caller owns Close
	issueExporter *mesh.PushIssueExporter // caller owns Close
	announcer     *mesh.Announcer         // registers the Contract Document, heartbeats
}

// newService assembles a meshed Benzene service: the caller's handlers, its event and send
// declarations (registerOutbound may be nil for a service that sends nothing), health-check
// interception, trace push to the collector, native routes, and the envelope endpoint at
// httpbinding.EnvelopePath (the default service standard's well-known mount, main repo's
// docs/specification/design-principles.md §5). provisionDescriptor controls the self-description -
// passing false is the "spec endpoint withheld" deployment: the service still traces, it just
// serves no document on benzene:spec (and on the view degrades to reduced, never breaks). The mesh
// wiring is the mesh.* lines; everything else is the same as examples/helloworld.
func newService(name, meshdEndpoint string, provisionDescriptor bool, registerHandlers func(*benzene.Registry), registerOutbound func(*contract.OutboundRegistry), routes []httpbinding.Route) *service {
	registry := benzene.NewRegistry()
	registerHandlers(registry)

	outbound := contract.NewOutboundRegistry()
	if registerOutbound != nil {
		registerOutbound(outbound)
	}

	info := mesh.ServiceInfo{Service: name, InstanceID: name + "-1"}
	// The Contract Document: what GET /benzene/spec and the benzene:spec topic answer with (R5), and
	// the body the announcer registers (mesh.md §2). A derivation error (a component-name collision)
	// is a wiring mistake: stop at start-up.
	spec, err := contract.Derive(registry, outbound, contract.Info{
		Service: name, ServiceVersion: "1.0.0", VersionScheme: "semver",
		MessageEndpoint: httpbinding.EnvelopePath, Transports: []string{"http"},
	})
	if err != nil {
		panic(err)
	}
	exporter := mesh.NewPushExporter(httpclient.NewClient(meshdEndpoint), mesh.PushExporterOptions{FlushInterval: time.Second})
	issueExporter := mesh.NewPushIssueExporter(httpclient.NewClient(meshdEndpoint), name, mesh.PushIssueExporterOptions{FlushInterval: time.Second})

	checks := []healthcheck.Check{healthcheck.NamedCheck("self", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "self"}
	})}

	middlewares := []benzene.Middleware{mesh.TraceMiddleware(info, exporter), mesh.IssueMiddleware(info, issueExporter)}
	if provisionDescriptor {
		middlewares = append(middlewares, contract.Middleware(spec))
	}
	middlewares = append(middlewares, healthcheck.Middleware(checks), benzene.RouterMiddleware(registry))

	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(middlewares...),
	}

	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	// The derived Contract Document (Cloud Service Profile R5) at the default /benzene/spec mount:
	// the GET dispatches benzene:spec through the pipeline, so a service that withholds its
	// self-description (provisionDescriptor false) answers it not-found like any unserved topic.
	mux.Handle(httpbinding.SpecPath, httpbinding.SpecHandler(builder))
	mux.Handle("/", httpbinding.Handler(builder, routes))
	return &service{
		handler:       mux,
		exporter:      exporter,
		issueExporter: issueExporter,
		announcer: mesh.NewAnnouncer(httpclient.NewClient(meshdEndpoint), spec, mesh.AnnouncerOptions{
			InstanceID: info.InstanceID,
			Checks:     checks,
			OnSendFailure: func(topic string, result benzene.Result[json.RawMessage]) {
				log.Printf("%s: %s to meshd: %v %v", name, topic, result.Status, result.Errors)
			},
		}),
	}
}

// newMeshd wires the collector's HTTP entry points on the default service standard's
// well-known paths: the envelope endpoint its topics are served on at
// httpbinding.EnvelopePath, the Mesh View at meshd.ViewPath, and a convenience redirect
// from / to the view.
func newMeshd() http.Handler {
	collector := meshd.New(meshd.Options{})
	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(collector.Builder()))
	mux.Handle(meshd.ViewPath, collector.ViewHandler(httpbinding.EnvelopePath))
	mux.Handle("/", http.RedirectHandler(meshd.ViewPath, http.StatusFound))
	return mux
}

func main() {
	go func() { log.Fatal(http.ListenAndServe(":"+meshdPort, newMeshd())) }()
	meshdEndpoint := "http://localhost:" + meshdPort + httpbinding.EnvelopePath

	greeter := newService("greeter", meshdEndpoint, true, func(registry *benzene.Registry) {
		benzene.MustRegister(registry, benzene.NewTopic("greet"), greetHandler)
	}, nil, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")},
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	})
	defer greeter.exporter.Close()
	defer greeter.issueExporter.Close()
	go func() { log.Fatal(http.ListenAndServe(":"+greeterPort, greeter.handler)) }()

	frontdoor := newService("frontdoor", meshdEndpoint, true, func(registry *benzene.Registry) {
		greeterClient := mesh.WithTraceContext(httpclient.NewClient("http://localhost:" + greeterPort + httpbinding.EnvelopePath))
		benzene.MustRegister(registry, benzene.NewTopic("welcome"), welcomeHandler(greeterClient))
	}, func(outbound *contract.OutboundRegistry) {
		// frontdoor's declared contract: it sends to greeter's "greet". This - not the trace
		// propagation above - is what makes "greet" show frontdoor as its provider on the
		// topic catalog (mesh.md §4).
		contract.MustRegisterSend[greetRequest](outbound, benzene.NewTopic("greet"), contract.WithResponse[greetResponse]())
	}, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/welcome", Topic: benzene.NewTopic("welcome")},
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	})
	defer frontdoor.exporter.Close()
	defer frontdoor.issueExporter.Close()
	go func() { log.Fatal(http.ListenAndServe(":"+frontdoorPort, frontdoor.handler)) }()

	// legacy-portal provisions ONLY the trace feed: no self-description (false below), and it
	// never registers or heartbeats - so even though it calls greeter, it has no registered
	// sends to show that edge with (mesh.md §4.2: an anonymous service has no
	// contract to diverge from, declared or drifted). It shows up on the view as reduced -
	// "missing feeds: descriptor, health" - and its calls to greeter still count in "greet"'s
	// invocation stats. This is the degradation rule, live.
	legacy := newService("legacy-portal", meshdEndpoint, false, func(registry *benzene.Registry) {
		greeterClient := mesh.WithTraceContext(httpclient.NewClient("http://localhost:" + greeterPort + httpbinding.EnvelopePath))
		benzene.MustRegister(registry, benzene.NewTopic("legacy:relay"), welcomeHandler(greeterClient))
	}, nil, []httpbinding.Route{
		{Method: http.MethodPost, Path: "/relay", Topic: benzene.NewTopic("legacy:relay")},
	})
	defer legacy.exporter.Close()
	defer legacy.issueExporter.Close()
	go func() { log.Fatal(http.ListenAndServe(":"+legacyPort, legacy.handler)) }()

	// Announce like the .NET example's StartAnnouncing: retry registration until the collector
	// (starting concurrently above) is up, then heartbeat every 10s (mesh.Announcer's defaults). A
	// registration that never lands is still only a reduced mesh, never a dead service.
	ctx := context.Background()
	go greeter.announcer.Run(ctx)
	go frontdoor.announcer.Run(ctx)

	log.Printf("mesh view      http://localhost:%s/", meshdPort)
	log.Printf("meshed flow    curl -s -X POST localhost:%s/welcome -d '{\"name\":\"Mesh\"}'", frontdoorPort)
	log.Printf("reduced flow   curl -s -X POST localhost:%s/relay -d '{\"name\":\"Mesh\"}'", legacyPort)
	log.Printf("contract       curl -s localhost:%s%s", greeterPort, httpbinding.SpecPath)
	select {}
}
