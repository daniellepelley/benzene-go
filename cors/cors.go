// Package cors is a portable, stdlib-only Cross-Origin Resource Sharing middleware for
// HTTP-fronted Benzene services - a Go port of the main daniellepelley/Benzene repo's own
// portable CORS middleware (src/Benzene.Http/Cors). CORS is an HTTP-transport concern (the
// Origin header, preflight OPTIONS, Access-Control-* headers), so unlike healthcheck.Middleware
// (a benzene.Middleware over the pipeline), this is an ordinary net/http middleware wrapping an
// http.Handler - it sits in front of whatever httpbinding.Handler (or any other http.Handler)
// produces.
package cors

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/daniellepelley/benzene-go/httpbinding"
)

// Settings configures Middleware.
type Settings struct {
	// AllowedOrigins is the list of origins allowed to make cross-origin requests. An entry
	// may be a full origin ("https://example.com" - matched exactly on scheme, host, and port,
	// since that triple is what the CORS spec calls an "origin") or a bare hostname
	// ("example.com" - matched on host only, ignoring scheme and port, as a more permissive
	// shorthand). "*" allows any origin; the matched request's own Origin value is always
	// echoed back rather than a literal "*", which browsers don't honor for credentialed
	// requests anyway.
	AllowedOrigins []string
	// AllowedHeaders is the list of request headers a preflight may ask for. "*" allows any
	// header - the response then echoes back whatever Access-Control-Request-Headers actually
	// asked for (equivalent to ASP.NET Core's AllowAnyHeader()), since browsers don't honor a
	// literal "*" for Access-Control-Allow-Headers on credentialed requests either.
	AllowedHeaders []string
	// ExposedHeaders is sent as Access-Control-Expose-Headers on actual (non-preflight)
	// responses - response headers, beyond the small CORS-safelisted set, that browser-side
	// JavaScript may read.
	ExposedHeaders []string
	// MaxAge, when positive, is sent as Access-Control-Max-Age on preflight responses: how long
	// a browser may cache the preflight result before sending another one. Zero omits the
	// header, and browsers fall back to their own (usually short) default.
	MaxAge time.Duration
	// AllowCredentials sets Access-Control-Allow-Credentials: true, permitting cross-origin
	// requests to include cookies or HTTP authentication. Safe to combine with a "*" entry in
	// AllowedOrigins, since the matched origin is always echoed back specifically (see
	// AllowedOrigins), which is what the CORS spec requires when credentials are allowed.
	AllowCredentials bool
}

// Middleware returns a net/http middleware that handles CORS for routes: it computes
// Access-Control-Allow-Methods per path from routes (the same route table passed to
// httpbinding.Handler), answers preflight OPTIONS requests directly (never reaching next), and
// adds the appropriate Access-Control-* headers to actual requests from an allowed origin.
// Register it ahead of httpbinding.Handler so it sees every request first.
func Middleware(settings Settings, routes []httpbinding.Route) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			methods := methodsForPath(routes, r.URL.Path)
			if len(methods) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// The response for this path differs by Origin (allowed vs. rejected, or which
			// origin is echoed back), so caches sitting in front of this endpoint must not
			// conflate responses for different origins.
			w.Header().Add("Vary", "Origin")

			matchedOrigin := matchOrigin(settings.AllowedOrigins, origin)
			allowed := matchedOrigin != "" && requestedHeadersAllowed(settings.AllowedHeaders, r)

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", matchedOrigin)
				w.Header().Set("Access-Control-Allow-Headers", resolveAllowedHeaders(settings.AllowedHeaders, r))
				w.Header().Set("Access-Control-Allow-Methods", "OPTIONS,"+strings.Join(methods, ","))
				if settings.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if r.Method == http.MethodOptions {
					if settings.MaxAge > 0 {
						w.Header().Set("Access-Control-Max-Age", strconv.Itoa(int(settings.MaxAge.Seconds())))
					}
				} else if len(settings.ExposedHeaders) > 0 {
					w.Header().Set("Access-Control-Expose-Headers", strings.Join(settings.ExposedHeaders, ","))
				}
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func methodsForPath(routes []httpbinding.Route, path string) []string {
	seen := make(map[string]bool, len(routes))
	var methods []string
	for _, route := range routes {
		if route.Path != path {
			continue
		}
		method := strings.ToUpper(route.Method)
		if seen[method] {
			continue
		}
		seen[method] = true
		methods = append(methods, method)
	}
	return methods
}

var recognizedSchemes = map[string]bool{"http": true, "https": true, "ws": true, "wss": true}

// matchOrigin returns origin itself if it matches an entry in allowedOrigins, else "".
func matchOrigin(allowedOrigins []string, origin string) string {
	for _, allowed := range allowedOrigins {
		if originMatches(allowed, origin) {
			return origin
		}
	}
	return ""
}

func originMatches(allowed, origin string) bool {
	if allowed == "*" {
		return true
	}

	allowedURI, allowedOK := parseOrigin(allowed)
	originURI, originOK := parseOrigin(origin)
	if allowedOK && originOK {
		return strings.EqualFold(allowedURI.Scheme, originURI.Scheme) &&
			strings.EqualFold(allowedURI.Host, originURI.Host)
	}

	return strings.EqualFold(hostOf(allowed), hostOf(origin))
}

// parseOrigin parses value as an absolute URL with a recognized scheme - i.e. a full origin
// like "https://example.com", not a bare hostname.
func parseOrigin(value string) (*url.URL, bool) {
	u, err := url.Parse(value)
	if err != nil || !u.IsAbs() || u.Host == "" || !recognizedSchemes[strings.ToLower(u.Scheme)] {
		return nil, false
	}
	return u, true
}

// hostOf returns value's hostname if it parses as a full origin, else value itself (trimmed of
// a trailing slash) treated as a bare hostname.
func hostOf(value string) string {
	if u, ok := parseOrigin(value); ok {
		return u.Hostname()
	}
	return strings.TrimSuffix(value, "/")
}

func containsWildcard(headers []string) bool {
	for _, h := range headers {
		if h == "*" {
			return true
		}
	}
	return false
}

func requestedHeadersAllowed(allowedHeaders []string, r *http.Request) bool {
	if containsWildcard(allowedHeaders) {
		return true
	}

	requested := r.Header.Get("Access-Control-Request-Headers")
	if requested == "" {
		return true
	}

	for _, header := range strings.Split(requested, ",") {
		header = strings.TrimSpace(header)
		if !containsFold(allowedHeaders, header) {
			return false
		}
	}
	return true
}

func containsFold(list []string, target string) bool {
	for _, item := range list {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

// resolveAllowedHeaders is what gets sent as Access-Control-Allow-Headers.
func resolveAllowedHeaders(allowedHeaders []string, r *http.Request) string {
	if !containsWildcard(allowedHeaders) {
		return strings.Join(allowedHeaders, ",")
	}
	if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
		return requested
	}
	return "*"
}
