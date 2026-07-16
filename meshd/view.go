package meshd

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed view.html
var viewHTML string

// Well-known paths from the default service standard (the main repo's
// docs/specification/design-principles.md §5). Defaults, not requirements - mount anywhere.
const (
	// ViewPath is the standard mount for ViewHandler, matching the .NET Fleet view's
	// /benzene/fleet-ui default.
	ViewPath = "/benzene/fleet-ui"
	// EnvelopePath is the standard wire-envelope mount ViewHandler polls when
	// envelopePath is "" (httpbinding.EnvelopePath, restated here so this package keeps
	// importing only the root module and mesh).
	EnvelopePath = "/benzene/invoke"
)

// ViewHandler serves the Mesh View (mesh.md §6, Phase 4): one self-contained page - no
// JS framework, no external assets, matching this module's zero-dependency stance - that
// polls the collector's own mesh:query:fleet topic through the wire-envelope endpoint
// mounted at envelopePath (same-origin; "" means the standard EnvelopePath). The page is
// a read-only rendering of FleetView: services with health and reduced-feed markers, the
// topic catalog with observed consumers, and recent flows.
//
// The view degrades like everything else in the mesh: if the envelope endpoint is
// unreachable it shows a retrying banner over the last rendered state rather than
// breaking, and an empty fleet renders as empty tables, not an error.
func (c *Collector) ViewHandler(envelopePath string) http.Handler {
	if envelopePath == "" {
		envelopePath = EnvelopePath
	}
	page := []byte(strings.ReplaceAll(viewHTML, "__ENVELOPE_PATH__", envelopePath))
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write(page) // a failed write is the client hanging up; nothing to do
	})
}
