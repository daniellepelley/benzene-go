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
// downstream service's in-cluster envelope endpoint. Each caller also registers an outbound
// record for its downstream topic (mesh.RegisterOutbound, mesh.md §2.3) — that declared
// Consumes entry, not trace propagation, is what makes the caller show up as a consumer on the
// mesh's topic catalog (mesh.md §4). The httpclient.Client is separately wrapped in
// mesh.WithTraceContext so the collector can additionally show that declared edge as observed
// (mesh.md §4.2, exactly like examples/mesh-helloworld's frontdoor -> greeter hop) — propagation
// layers on top of the declared edge, it never substitutes for it. shipping is terminal: no
// DOWNSTREAM_MSG_URL, no outbound client, nothing to register.
//
// Fleet reporting: when MESH_COLLECTOR_ENVELOPE_URL is set, this service announces its
// descriptor, heartbeats every 10s, and pushes traces + issues to the mesh's meshd collector —
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
	descriptor    mesh.Descriptor
	exporter      *mesh.PushExporter      // nil-safe; nil when no collector is configured
	issueExporter *mesh.PushIssueExporter // nil-safe; nil when no collector is configured
	collector     *httpclient.Client      // nil when no collector is configured
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
	outbound := mesh.NewOutboundRegistry()
	routes := []httpbinding.Route{
		{Method: http.MethodGet, Path: httpbinding.HealthPath, Topic: benzene.NewTopic(healthcheck.ReservedTopic)},
	}
	name, routes := registerDomain(registry, outbound, routes, meshService, downstream)

	info := mesh.ServiceInfo{Service: name, ServiceVersion: "1.0.0", InstanceID: name, Binding: "http"}
	descriptor := mesh.Describe(registry, outbound, info)

	checks := []healthcheck.Check{healthcheck.NamedCheck("self", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "self", Data: map[string]any{"service": name}}
	})}

	// Fleet reporting is entirely optional and best-effort, matching benzene-dotnet's
	// WithCollector: an unreachable or unconfigured collector reduces the mesh, never this
	// service. exporter/issueExporter stay nil when collectorURL is "", and
	// mesh.TraceMiddleware/mesh.IssueMiddleware are documented nil-safe against that.
	var exporter *mesh.PushExporter
	var issueExporter *mesh.PushIssueExporter
	var collector *httpclient.Client
	if collectorURL != "" {
		collector = httpclient.NewClient(collectorURL)
		exporter = mesh.NewPushExporter(collector, mesh.PushExporterOptions{FlushInterval: 5 * time.Second})
		issueExporter = mesh.NewPushIssueExporter(collector, name, mesh.PushIssueExporterOptions{FlushInterval: 5 * time.Second})
	}

	pipeline := benzene.NewPipeline(
		mesh.TraceMiddleware(info, exporter),
		mesh.IssueMiddleware(info, issueExporter),
		mesh.Middleware(descriptor),
		healthcheck.Middleware(checks),
		benzene.RouterMiddleware(registry),
	)
	builder := &benzene.ApplicationBuilder{Registry: registry, Container: benzene.NewContainer(), Pipeline: pipeline}

	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	mux.Handle(httpbinding.SpecPath, mesh.SpecHandler(descriptor))
	mux.Handle("/", httpbinding.Handler(builder, routes))

	return &service{
		name:          name,
		handler:       mux,
		descriptor:    descriptor,
		exporter:      exporter,
		issueExporter: issueExporter,
		collector:     collector,
	}
}

// registerDomain wires the selected domain's handler + native route onto registry/routes, and -
// when downstream is non-nil - its outbound registration for the topic it chains to onto
// outbound (mesh.md §2.3: the declared Consumes entry, the sole source of that consumer edge on
// the mesh's topic catalog, mesh.md §4). Returns the resolved service name ("orders" is also the
// default for any unrecognised value, matching benzene-dotnet's Domain.HandlersFor switch
// default).
func registerDomain(registry *benzene.Registry, outbound *mesh.OutboundRegistry, routes []httpbinding.Route, meshService string, downstream client.Sender) (string, []httpbinding.Route) {
	switch meshService {
	case "payments":
		if err := benzene.Register(registry, benzene.NewTopic(domain.TopicPaymentTake), domain.TakePaymentHandler(downstream)); err != nil {
			log.Fatalf("register payment:take: %v", err)
		}
		routes = append(routes, httpbinding.Route{Method: http.MethodPost, Path: "/payments", Topic: benzene.NewTopic(domain.TopicPaymentTake)})
		if downstream != nil {
			if err := mesh.RegisterOutbound[domain.BookShipmentRequest, domain.ShipmentBooked](outbound, benzene.NewTopic(domain.TopicShipmentBook)); err != nil {
				log.Fatalf("register outbound shipment:book: %v", err)
			}
		}
		return "payments", routes
	case "shipping":
		if err := benzene.Register(registry, benzene.NewTopic(domain.TopicShipmentBook), domain.BookShipmentHandler()); err != nil {
			log.Fatalf("register shipment:book: %v", err)
		}
		routes = append(routes, httpbinding.Route{Method: http.MethodPost, Path: "/shipments", Topic: benzene.NewTopic(domain.TopicShipmentBook)})
		// shipping is terminal: no downstream, nothing to register as consumed.
		return "shipping", routes
	default:
		if err := benzene.Register(registry, benzene.NewTopic(domain.TopicOrderCreate), domain.CreateOrderHandler(downstream)); err != nil {
			log.Fatalf("register order:create: %v", err)
		}
		routes = append(routes, httpbinding.Route{Method: http.MethodPost, Path: "/orders", Topic: benzene.NewTopic(domain.TopicOrderCreate)})
		if downstream != nil {
			if err := mesh.RegisterOutbound[domain.TakePaymentRequest, domain.PaymentTaken](outbound, benzene.NewTopic(domain.TopicPaymentTake)); err != nil {
				log.Fatalf("register outbound payment:take: %v", err)
			}
		}
		return "orders", routes
	}
}

// announceAndHeartbeat registers s.descriptor with the collector (retrying while it comes up,
// matching examples/mesh-helloworld's announce/heartbeat loop), then heartbeats every 10s until
// ctx is cancelled. A no-op when s.collector is nil (no MESH_COLLECTOR_ENVELOPE_URL configured).
func (s *service) announceAndHeartbeat(ctx context.Context) {
	if s.collector == nil {
		return
	}
	for attempt := 0; attempt < 30 && !s.announce(ctx); attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	s.heartbeat(ctx)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.heartbeat(ctx)
		}
	}
}

// announce registers s.descriptor with the collector once, reporting whether it landed. Failure
// is logged and otherwise ignored — an unreachable collector reduces the mesh, never this
// service (matching examples/mesh-helloworld's service.announce).
func (s *service) announce(ctx context.Context) bool {
	body, err := json.Marshal(s.descriptor)
	if err != nil {
		log.Printf("%s: marshal descriptor: %v", s.name, err)
		return false
	}
	if result := s.collector.Send(ctx, benzene.NewTopic(mesh.TopicRegister), nil, body); !result.IsSuccessful() {
		log.Printf("%s: register with collector: %v %v", s.name, result.Status, result.Errors)
		return false
	}
	return true
}

// heartbeat sends one health report. In a real service the Health field would come from running
// the same checks the healthcheck middleware serves; a static healthy report keeps the example
// focused (matching examples/mesh-helloworld's own heartbeat).
func (s *service) heartbeat(ctx context.Context) {
	hb, err := json.Marshal(mesh.Heartbeat{
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
	if result := s.collector.Send(ctx, benzene.NewTopic(mesh.TopicHeartbeat), nil, hb); !result.IsSuccessful() {
		log.Printf("%s: heartbeat to collector: %v %v", s.descriptor.Service, result.Status, result.Errors)
	}
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

	if svc.collector != nil {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go svc.announceAndHeartbeat(ctx)
	}

	log.Printf("%s listening on :%s (downstream=%q collector=%q)", svc.name, port, downstreamURL, collectorURL)
	log.Fatal(http.ListenAndServe(":"+port, svc.handler))
}
