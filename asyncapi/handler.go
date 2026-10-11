package asyncapi

import (
	"encoding/json"
	"net/http"

	"github.com/daniellepelley/benzene-go/contract"
)

// Handler serves the AsyncAPI 3.0 document derived from doc over a plain GET, the event-driven
// sibling of openapi.Handler. (The same document can instead be served on benzene:spec as a port
// extension: contract.Middleware(doc, contract.WithDocumentType("asyncapi", "json", asyncapi.Generate(doc))).) Mount it wherever a service wants to expose its
// AsyncAPI doc (e.g. GET /asyncapi.json). Non-GET/HEAD requests get 405; the document is generated
// once at construction, not per request, since the document is static after startup.
func Handler(doc contract.Document, opts ...Option) http.Handler {
	body, err := json.Marshal(Generate(doc, opts...))
	if err != nil {
		// The document is composed only of strings, maps, and slices of those, so json.Marshal cannot
		// fail here; fall back to an empty object rather than panicking a server at startup.
		body = []byte("{}")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(body)
	})
}
