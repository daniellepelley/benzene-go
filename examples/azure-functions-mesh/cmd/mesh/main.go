// Command azure-functions-mesh-mesh is the seventh Azure Function of examples/azure-functions-mesh:
// the mesh itself. Unlike .NET's examples/AzureFunctionsMesh/Mesh, this port's mesh does not
// discover the six service Functions by listing/tagging and interrogating them over ARM (no
// Azure Resource Manager discovery provider, no Blob-backed catalog store exist in this port) -
// it is a thin wrapper around meshd.Collector, reusing examples/k8s-mesh-helloworld's and
// examples/aws-lambda-mesh's push-based collector pattern verbatim. See
// examples/azure-functions-mesh/README.md's "Divergence from .NET" section for the full story.
//
// # Trigger surface: HTTP only, no timer
//
// .NET's mesh Function has TWO triggers: a catch-all HTTP trigger (serving the Mesh UI + catalog
// artifacts) and a TIMER trigger running the discovery + aggregation pass on a schedule - it
// needs the timer because it PULLS (lists/interrogates the tagged sites). This port's mesh is
// pure PUSH: the six services call it, it never calls out anywhere, so there is nothing to poll
// on a schedule - the same HTTP-only surface examples/k8s-mesh-helloworld's cmd/mesh (a plain
// Kubernetes Service) and examples/aws-lambda-mesh's mesh Lambda (API Gateway + direct Invoke,
// also no schedule) already settle on. So this Function's local paths are all HTTP-triggered:
//
//   - GET  /benzene/fleet-ui  - the Mesh View (meshd.Collector.ViewHandler), wrapped for the
//     custom-handler contract via meshapp.WrapRawHandler since ViewHandler expects a real
//     *http.Request/http.ResponseWriter, not the topic-routed dispatch every other surface here
//     uses.
//   - POST /benzene/invoke     - the wire-envelope endpoint (meshapp.EnvelopeHandler): what the six
//     services push register/heartbeat/trace/issue reports to, AND what the Fleet View's own
//     same-origin JS polls for benzene:mesh:query:fleet.
//   - GET  /mesh/discovered    - a tiny convenience wrapper this example adds (matching
//     examples/k8s-mesh-helloworld's and examples/aws-lambda-mesh's own "/mesh/discovered") so a
//     human or CI can assert a service count with one curl: {"discovered":N} - a read of the
//     already-live push feed, not a trigger for a discovery pass (there is no pull-based pass to
//     trigger here).
package main

import (
	"encoding/json"
	"log"
	"net/http"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/mesh"
	"github.com/daniellepelley/benzene-go/meshd"

	"github.com/daniellepelley/benzene-go/azurefunctions"
	"github.com/daniellepelley/benzene-go/examples/azure-functions-mesh/meshapp"
)

// discoveredResponse mirrors examples/k8s-mesh-helloworld's and examples/aws-lambda-mesh's own
// {"discovered":N} convenience shape, so every push-based mesh example answers the same way to
// the same kind of curl.
type discoveredResponse struct {
	Discovered int `json:"discovered"`
}

// discoveredHandler answers {"discovered":N} where N is the number of services currently in the
// collector's fleet view - an in-process read of the same benzene:mesh:query:fleet topic the
// Fleet View itself polls, via envelope.DispatchTopicResult (no HTTP round trip needed; this
// binary IS the collector). Answered through meshapp.WriteInvocationResponse directly (not
// meshapp.EnvelopeHandler, which speaks the wire envelope, or WrapRawHandler, which expects a raw
// net/http.Handler) since this is neither - a small custom-handler response of its own.
func discoveredHandler(builder *benzene.ApplicationBuilder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, _ := envelope.DispatchTopicResult(r.Context(), builder.Pipeline, builder.Container, benzene.NewTopic(mesh.TopicQueryFleet), nil, "")
		var fleet meshd.FleetView
		_ = json.Unmarshal([]byte(resp.Body), &fleet)
		data, err := json.Marshal(discoveredResponse{Discovered: len(fleet.Services)})
		if err != nil {
			meshapp.WriteInvocationResponse(w, http.StatusInternalServerError, "failed to serialize discovered response", nil)
			return
		}
		meshapp.WriteInvocationResponse(w, http.StatusOK, string(data), map[string]string{"content-type": "application/json"})
	})
}

// mux builds the mesh Function's custom-handler HTTP server - see the package doc for why every
// local path here is HTTP-triggered.
func mux(collector *meshd.Collector) http.Handler {
	builder := collector.Builder()

	m := http.NewServeMux()
	m.Handle("/FleetUi", meshapp.WrapRawHandler(collector.ViewHandler(httpbinding.EnvelopePath)))
	m.Handle("/Invoke", meshapp.EnvelopeHandler(builder))
	m.Handle("/Discovered", discoveredHandler(builder))
	return m
}

func main() {
	collector := meshd.New(meshd.Options{})
	addr := azurefunctions.ListenAddr()
	log.Printf("mesh listening on %s (view %s, envelope %s)", addr, meshd.ViewPath, httpbinding.EnvelopePath)
	log.Fatal(http.ListenAndServe(addr, mux(collector)))
}
