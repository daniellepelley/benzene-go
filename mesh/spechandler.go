package mesh

import (
	"encoding/json"
	"net/http"
)

// SpecHandler serves the service's derived spec document over HTTP for the Cloud Service Profile's
// R5 (docs/specification/cloud-service-profile.md §2). The default service standard mounts it at
// httpbinding.SpecPath ("/benzene/spec"; design-principles.md §5.2).
//
// The spec document IS the service Descriptor - the registry-derived topic catalog with
// request/response JSON schemas (mesh.md §5.1). The profile does not mandate a particular spec
// format (OpenAPI, AsyncAPI, or Benzene's own), only that a real derived document is exposed; the
// descriptor is exactly that, and because it is derived from the registry it can never go stale
// (design-principles.md §3). This is the same truth the reserved benzene:mesh topic serves for R6,
// offered here as a plain GET surface for tooling that wants the spec without speaking the wire
// envelope.
//
// Only GET is served (405 otherwise). Provisioning it is opt-in, exactly like the descriptor
// Middleware: a deployment that must not expose the spec (e.g. pending a security review,
// design-principles.md §5.1) simply doesn't mount it, and the rest of the mesh keeps working.
func SpecHandler(descriptor Descriptor) http.Handler {
	// Marshalled once at construction: a Descriptor is plain startup-derived data (strings, maps,
	// slices), so this never fails, and serving a request is then just a byte write.
	body, _ := json.Marshal(descriptor)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}
