package cors

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

func testRoutes() []httpbinding.Route {
	return []httpbinding.Route{
		{Method: http.MethodGet, Path: "/items", Topic: benzene.NewTopic("list-items")},
		{Method: http.MethodPost, Path: "/items", Topic: benzene.NewTopic("create-item")},
	}
}

func recordingHandler() (http.Handler, *bool) {
	called := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), &called
}

func TestMiddleware_NoOriginHeaderPassesThrough(t *testing.T) {
	next, called := recordingHandler()
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}}, testRoutes())(next)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !*called {
		t.Error("next should be called when there is no Origin header")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("no CORS headers should be set without an Origin header")
	}
}

func TestMiddleware_UnknownPathPassesThrough(t *testing.T) {
	next, called := recordingHandler()
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}}, testRoutes())(next)

	req := httptest.NewRequest(http.MethodGet, "/no-such-path", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !*called {
		t.Error("next should be called for a path with no registered routes")
	}
	if rec.Header().Get("Vary") != "" {
		t.Error("Vary should not be set for a path with no registered routes")
	}
}

func TestMiddleware_WildcardOriginEchoesRequestOrigin(t *testing.T) {
	next, called := recordingHandler()
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}}, testRoutes())(next)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !*called {
		t.Error("next should be called for an allowed non-OPTIONS request")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the echoed request origin, not a literal *", got)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Errorf("Vary = %q, want %q", rec.Header().Get("Vary"), "Origin")
	}
}

func TestMiddleware_ExactOriginMatchOnSchemeHostPort(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"https://example.com:8443"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com:8443")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com:8443" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://example.com:8443")
	}
}

func TestMiddleware_MismatchedSchemeIsRejected(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"https://example.com"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a scheme mismatch", got)
	}
}

func TestMiddleware_MismatchedPortIsRejected(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"https://example.com:8443"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com:9000")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a port mismatch", got)
	}
}

func TestMiddleware_BareHostnameMatchesAnyPortOrScheme(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"example.com"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com:8443")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com:8443" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the request origin echoed back", got)
	}
}

func TestMiddleware_UnlistedOriginIsRejected(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"https://example.com"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for an unlisted origin", got)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Error("Vary: Origin should still be set even when the origin is rejected")
	}
}

func TestMiddleware_PreflightRequestIsAnsweredWithoutCallingNext(t *testing.T) {
	next, called := recordingHandler()
	handler := Middleware(Settings{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"content-type"},
	}, testRoutes())(next)

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if *called {
		t.Error("next should not be called for a preflight OPTIONS request")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	methods := rec.Header().Get("Access-Control-Allow-Methods")
	if methods != "OPTIONS,GET,POST" {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q", methods, "OPTIONS,GET,POST")
	}
}

func TestMiddleware_RejectedPreflightStillReturns200(t *testing.T) {
	next, called := recordingHandler()
	handler := Middleware(Settings{AllowedOrigins: []string{"https://example.com"}}, testRoutes())(next)

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if *called {
		t.Error("next should not be called for an OPTIONS request, allowed or not")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d - the browser enforces rejection via the missing header, not a non-200", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("Access-Control-Allow-Origin should not be set for a rejected origin")
	}
}

func TestMiddleware_WildcardAllowedHeadersEchoesRequested(t *testing.T) {
	handler := Middleware(Settings{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"*"},
	}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Headers", "x-custom, content-type")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "x-custom, content-type" {
		t.Errorf("Access-Control-Allow-Headers = %q, want the echoed request value", got)
	}
}

func TestMiddleware_WildcardAllowedHeadersWithNoRequestGivesLiteralWildcard(t *testing.T) {
	handler := Middleware(Settings{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"*"},
	}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "*" {
		t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, "*")
	}
}

func TestMiddleware_DisallowedRequestedHeaderRejectsOrigin(t *testing.T) {
	handler := Middleware(Settings{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"content-type"},
	}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Headers", "x-forbidden")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty - a disallowed requested header rejects the whole preflight", got)
	}
}

func TestMiddleware_ExplicitAllowedHeadersAreJoined(t *testing.T) {
	handler := Middleware(Settings{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"content-type", "authorization"},
	}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "content-type,authorization" {
		t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, "content-type,authorization")
	}
}

func TestMiddleware_AllowCredentialsSetsHeader(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}, AllowCredentials: true}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
	}
}

func TestMiddleware_NoAllowCredentialsOmitsHeader(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
}

func TestMiddleware_MaxAgeSetOnlyOnPreflight(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}, MaxAge: 10 * time.Minute}, testRoutes())(passThrough())

	preflight := httptest.NewRequest(http.MethodOptions, "/items", nil)
	preflight.Header.Set("Origin", "https://example.com")
	preflightRec := httptest.NewRecorder()
	handler.ServeHTTP(preflightRec, preflight)
	if got := preflightRec.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Errorf("Access-Control-Max-Age = %q, want %q", got, "600")
	}

	actual := httptest.NewRequest(http.MethodGet, "/items", nil)
	actual.Header.Set("Origin", "https://example.com")
	actualRec := httptest.NewRecorder()
	handler.ServeHTTP(actualRec, actual)
	if got := actualRec.Header().Get("Access-Control-Max-Age"); got != "" {
		t.Errorf("Access-Control-Max-Age = %q, want empty on a non-preflight request", got)
	}
}

func TestMiddleware_ZeroMaxAgeOmitsHeader(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Max-Age"); got != "" {
		t.Errorf("Access-Control-Max-Age = %q, want empty when MaxAge is unset", got)
	}
}

func TestMiddleware_ExposedHeadersSetOnlyOnActualRequest(t *testing.T) {
	handler := Middleware(Settings{
		AllowedOrigins: []string{"*"},
		ExposedHeaders: []string{"x-total-count", "x-page"},
	}, testRoutes())(passThrough())

	actual := httptest.NewRequest(http.MethodGet, "/items", nil)
	actual.Header.Set("Origin", "https://example.com")
	actualRec := httptest.NewRecorder()
	handler.ServeHTTP(actualRec, actual)
	if got := actualRec.Header().Get("Access-Control-Expose-Headers"); got != "x-total-count,x-page" {
		t.Errorf("Access-Control-Expose-Headers = %q, want %q", got, "x-total-count,x-page")
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/items", nil)
	preflight.Header.Set("Origin", "https://example.com")
	preflightRec := httptest.NewRecorder()
	handler.ServeHTTP(preflightRec, preflight)
	if got := preflightRec.Header().Get("Access-Control-Expose-Headers"); got != "" {
		t.Errorf("Access-Control-Expose-Headers = %q, want empty on a preflight request", got)
	}
}

func TestMiddleware_NoExposedHeadersOmitsHeader(t *testing.T) {
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}}, testRoutes())(passThrough())

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "" {
		t.Errorf("Access-Control-Expose-Headers = %q, want empty", got)
	}
}

func TestMiddleware_DuplicateMethodForSamePathIsNotRepeated(t *testing.T) {
	routes := []httpbinding.Route{
		{Method: http.MethodGet, Path: "/items", Topic: benzene.NewTopic("list-items")},
		{Method: "get", Path: "/items", Topic: benzene.NewTopic("list-items-alias")},
	}
	handler := Middleware(Settings{AllowedOrigins: []string{"*"}}, routes)(passThrough())

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "OPTIONS,GET" {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q (GET listed once)", got, "OPTIONS,GET")
	}
}

func passThrough() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}
