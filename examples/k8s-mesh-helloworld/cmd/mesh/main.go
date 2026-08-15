// Command k8s-mesh-collector is the mesh service of the k8s-mesh-helloworld example: a thin
// wrapper around meshd.Collector, the Go counterpart of benzene-dotnet's examples/K8sMesh/Mesh —
// with one deliberate, documented divergence. See ../../README.md's "Divergence from .NET"
// section for the full story; in short: benzene-go has no Kubernetes API service-discovery
// library (no Benzene.Mesh.Discovery.Kubernetes equivalent), so this binary does not list or
// interrogate Kubernetes Services. Instead it is purely push-based — orders/payments/shipping
// each report to it directly (register/heartbeat/traces/issues over MESH_COLLECTOR_ENVELOPE_URL,
// see cmd/service/main.go) — which is exactly the live "Fleet plane" half of the .NET example's
// mesh, minus the pull-based catalog half.
//
// Surfaces:
//   - POST /benzene/invoke — the collector's wire-envelope endpoint: every benzene:mesh:* topic
//     (register/heartbeat/traces/issues/query:*), the same endpoint the three domain services
//     push to and the same one meshd.ViewHandler polls.
//   - GET  /benzene/fleet-ui — the Mesh View (meshd.ViewHandler): services, topics, and recent
//     flows, all derived from what has actually registered/heartbeated/traced — nothing declared.
//   - GET  /mesh/discovered — a tiny convenience wrapper this example adds so CI (and a human)
//     can assert a service count with one curl, the same shape as .NET's POST /mesh/refresh
//     answering {"discovered":3} — except this is a read of the already-live push feed, not a
//     trigger for a discovery pass (there is no pull-based pass to trigger here).
//   - GET / redirects to the Mesh View.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// discoveredResponse mirrors benzene-dotnet's MeshRefreshResult shape ({"discovered":N}) so the
// kind/EKS workflows' assertions read the same way across ports, even though what "discovered"
// means differs (see the package comment).
type discoveredResponse struct {
	Discovered int `json:"discovered"`
}

// discoveredHandler answers {"discovered":N} where N is the number of services currently in the
// collector's fleet view — an in-process read of the same benzene:mesh:query:fleet topic the
// Mesh View itself polls, via envelope.DispatchTopicResult (no HTTP round trip needed; this
// binary IS the collector).
func discoveredHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, _ := envelope.DispatchTopicResult(r.Context(), builder.Pipeline, builder.Container, benzene.NewTopic(mesh.TopicQueryFleet), nil, "")
		var fleet meshd.FleetView
		_ = json.Unmarshal([]byte(resp.Body), &fleet)
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(discoveredResponse{Discovered: len(fleet.Services)})
	})
}

// newMeshApp builds the mesh collector's http.Handler — split out from main() so a test can
// exercise it directly against httptest, the same seam cmd/service's newService offers.
func newMeshApp() http.Handler {
	collector := meshd.New(meshd.Options{})
	builder := collector.Builder()

	mux := http.NewServeMux()
	mux.Handle(httpbinding.EnvelopePath, httpbinding.EnvelopeHandler(builder))
	mux.Handle(meshd.ViewPath, collector.ViewHandler(httpbinding.EnvelopePath))
	mux.Handle("/mesh/discovered", discoveredHandler(builder))
	mux.Handle("/", http.RedirectHandler(meshd.ViewPath, http.StatusFound))
	return mux
}

func main() {
	port := envOr("PORT", "8080")
	log.Printf("mesh collector listening on :%s (view %s, envelope %s)", port, meshd.ViewPath, httpbinding.EnvelopePath)
	log.Fatal(http.ListenAndServe(":"+port, newMeshApp()))
}
