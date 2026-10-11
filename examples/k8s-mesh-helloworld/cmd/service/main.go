// Command k8s-mesh-service is one of three domain services — orders, payments, shipping —
// selected at startup by the MESH_SERVICE env var: the Go counterpart of benzene-dotnet's
// examples/K8sMesh/Service. All three ship as the SAME image, deployed three times
// (see ../../k8s/services.yaml); only the selected domain's handler is registered, so each
// deployed Kubernetes Service exposes its own topic and the mesh's fleet view shows real
// per-service ownership.
//
// Ingress: every instance exposes the wire-envelope endpoint (httpbinding.EnvelopePath,
// "/benzene/invoke") — a { topic, headers, body } envelope POSTed there is routed to this
// service's handler by the envelope's topic, exactly like a queue or a Lambda invoke would. This
// is the Go binding's equivalent of the .NET example's POST /benzene-message; the endpoint name
// differs, the shape and role are identical. Plus GET /benzene/health (Cloud Service Profile).
//
// Egress (the chain): orders' order:create handler asks payments' payment:take, and payments'
// payment:take handler asks shipping's shipment:book, each over DOWNSTREAM_MSG_URL — the
// downstream service's in-cluster envelope endpoint. Each caller also declares a send to its
// downstream topic (contract.RegisterSend) — that declared send, not trace propagation, is what
// makes the caller that topic's provider on the mesh's topic catalog (mesh.md §4). The httpclient.Client is separately wrapped in
// mesh.WithTraceContext so the collector can additionally show that declared edge as observed
// (mesh.md §4.2, exactly like examples/mesh-helloworld's frontdoor -> greeter hop) — propagation
// layers on top of the declared edge, it never substitutes for it. shipping is terminal: no
// DOWNSTREAM_MSG_URL, no outbound client, nothing to register.
//
// Fleet reporting: when MESH_COLLECTOR_ENVELOPE_URL is set, this service registers its Contract
// Document (mesh.Announcer - the same document benzene:spec serves), heartbeats every 10s, and
// pushes traces + issues to the mesh's meshd collector (or any other port's collector) —
// the same push-based fleet plane benzene-dotnet's K8sMesh services use, over
// mesh.PushExporter/PushIssueExporter instead of Benzene.Mesh.Collector's client. See
// examples/k8s-mesh-helloworld/README.md's "Divergence from .NET" section for why this port has
// no pull-based discovery counterpart.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/examples/k8s-mesh-helloworld/domain"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/httpclient"
	"github.com/daniellepelley/benzene-go/mesh"
)

// service bundles one running instance: its HTTP handler plus everything main needs to
// announce/heartbeat it and shut it down cleanly. Kept separate from main() so tests can build
// one directly against httptest servers, the same shape examples/mesh-helloworld's newService
// test seam uses.
type service struct {
	name          string
	handler       http.Handler
	contract      contract.Document
	announcer     *mesh.Announcer         // nil-safe; nil when no collector is configured
	exporter      *mesh.PushExporter      // nil-safe; nil when no collector is configured
	issueExporter *mesh.PushIssueExporter // nil-safe; nil when no collector is configured
}

// newService wires one domain instance: the selected handler + native route, fleet-reporting
// middleware (a no-op pass-through when collectorURL is ""), and an optional trace-context-
// propagating downstream client (nil when downstreamURL is ""). meshService selects the domain
// exactly like benzene-dotnet's Domain.HandlersFor switch — any value other than "payments"/
// "shipping" resolves to "orders" (the default).
func newService(meshService, downstreamURL, collectorURL string) *service {
	var downstream client.Sender
	if downstreamURL != "" {
		downstream = mesh.WithTraceContext(httpclient.NewClient(downstreamURL))
	}

	registry := benzene.NewRegistry()
	outbound := contract.NewOutboundRegistry()
	routes := []httpbinding.Route{
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	}
	name, routes := registerDomain(registry, outbound, routes, meshService, downstream)

	info := mesh.ServiceInfo{Service: name, InstanceID: name}
	// The Contract Document: served on benzene:spec and GET /benzene/spec (R5), and registered with
	// the collector (mesh.md §2) - one derivation for both. A derivation error - a component-name
	// collision - is a wiring mistake, so it stops the service at start-up.
	placement := mesh.DetectPlacement()
	spec, err := contract.Derive(registry, outbound, contract.Info{
		Service: name, ServiceVersion: "1.0.0", VersionScheme: "semver", Placement: &placement,
		MessageEndpoint: httpbinding.EnvelopePath, Transports: []string{"http"}, HTTPRoutes: contractRoutes(routes),
	})
	if err != nil {
		panic(err)
	}

	checks := []healthcheck.Check{healthcheck.NamedCheck("self", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "self", Data: map[string]any{"service": name}}
	})}

	// Fleet reporting is entirely optional and best-effort, matching benzene-dotnet's
	// WithCollector: an unreachable or unconfigured collector reduces the mesh, never this
	// service. exporter/issueExporter stay nil when collectorURL is "", and
	// mesh.TraceMiddleware/mesh.IssueMiddleware are documented nil-safe against that.
	var announcer *mesh.Announcer
	var exporter *mesh.PushExporter
	var issueExporter *mesh.PushIssueExporter
	if collectorURL != "" {
		collector := httpclient.NewClient(collectorURL)
		announcer = mesh.NewAnnouncer(collector, spec, mesh.AnnouncerOptions{
			InstanceID: name,
			Checks:     checks,
			OnSendFailure: func(topic string, result benzene.Result[json.RawMessage]) {
				log.Printf("%s: %s to collector: %v %v", name, topic, result.Status, result.Errors)
			},
		})
		exporter = mesh.NewPushExporter(collector, mesh.PushExporterOptions{FlushInterval: 5 * time.Second})
		issueExporter = mesh.NewPushIssueExporter(collector, name, mesh.PushIssueExporterOptions{FlushInterval: 5 * time.Second})
	}

	pipeline := benzene.NewPipeline(
		mesh.TraceMiddleware(info, exporter),
		mesh.IssueMiddleware(info, issueExporter),
		contract.Middleware(spec),
		healthcheck.Middleware(checks),
		benzene.RouterMiddleware(registry),
	)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: pipeline}

	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	mux.Handle(httpbinding.SpecPath, httpbinding.SpecHandler(builder))
	mux.Handle("/", httpbinding.Handler(builder, routes))

	return &service{
		name:          name,
		handler:       mux,
		contract:      spec,
		announcer:     announcer,
		exporter:      exporter,
		issueExporter: issueExporter,
	}
}

// registerDomain wires the selected domain's handler + native route onto registry/routes, and -
// when downstream is non-nil - its declared send to the topic it chains to onto outbound (the sole
// source of that provider edge on the mesh's topic catalog, mesh.md §4). Returns the resolved service name ("orders" is also the
// default for any unrecognised value, matching benzene-dotnet's Domain.HandlersFor switch
// default).
func registerDomain(registry *benzene.Registry, outbound *contract.OutboundRegistry, routes []httpbinding.Route, meshService string, downstream client.Sender) (string, []httpbinding.Route) {
	switch meshService {
	case "payments":
		benzene.MustRegister(registry, benzene.NewTopic(domain.TopicPaymentTake), domain.TakePaymentHandler(downstream))
		routes = append(routes, httpbinding.Route{Method: http.MethodPost, Path: "/payments", Topic: benzene.NewTopic(domain.TopicPaymentTake)})
		if downstream != nil {
			contract.MustRegisterSend[domain.BookShipmentRequest](outbound, benzene.NewTopic(domain.TopicShipmentBook), contract.WithResponse[domain.ShipmentBooked]())
		}
		return "payments", routes
	case "shipping":
		benzene.MustRegister(registry, benzene.NewTopic(domain.TopicShipmentBook), domain.BookShipmentHandler())
		routes = append(routes, httpbinding.Route{Method: http.MethodPost, Path: "/shipments", Topic: benzene.NewTopic(domain.TopicShipmentBook)})
		// shipping is terminal: no downstream, nothing it sends.
		return "shipping", routes
	default:
		benzene.MustRegister(registry, benzene.NewTopic(domain.TopicOrderCreate), domain.CreateOrderHandler(downstream))
		routes = append(routes, httpbinding.Route{Method: http.MethodPost, Path: "/orders", Topic: benzene.NewTopic(domain.TopicOrderCreate)})
		if downstream != nil {
			contract.MustRegisterSend[domain.TakePaymentRequest](outbound, benzene.NewTopic(domain.TopicPaymentTake), contract.WithResponse[domain.PaymentTaken]())
		}
		return "orders", routes
	}
}

// contractRoutes turns the HTTP routes into the Contract Document's httpMappings input.
func contractRoutes(routes []httpbinding.Route) []contract.HTTPRoute {
	out := make([]contract.HTTPRoute, len(routes))
	for i, r := range routes {
		out[i] = contract.HTTPRoute{Method: r.Method, Path: r.Path, Topic: r.Topic}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	port := envOr("PORT", "8080")
	downstreamURL := os.Getenv("DOWNSTREAM_MSG_URL")
	collectorURL := os.Getenv("MESH_COLLECTOR_ENVELOPE_URL")

	svc := newService(envOr("MESH_SERVICE", "orders"), downstreamURL, collectorURL)
	defer svc.exporter.Close()
	defer svc.issueExporter.Close()

	// Register (retrying while the collector comes up), then heartbeat every 10s. A no-op when no
	// collector is configured: the announcer is nil.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.announcer.Run(ctx)

	log.Printf("%s listening on :%s (downstream=%q collector=%q)", svc.name, port, downstreamURL, collectorURL)
	log.Fatal(http.ListenAndServe(":"+port, svc.handler))
}
