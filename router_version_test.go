package benzene

import (
	"context"
	"testing"
)

// versionEcho registers a handler for topic that answers with a message naming which handler
// ran, so a test can tell an exact-version match from an unversioned fallback.
func versionEcho(t *testing.T, registry *Registry, topic Topic, label string) {
	t.Helper()
	h := Handler[helloRequest, helloResponse](func(_ context.Context, req helloRequest) Result[helloResponse] {
		return Ok(helloResponse{Message: label + ":" + req.Name})
	})
	if err := Register(registry, topic, h); err != nil {
		t.Fatalf("Register(%v) error = %v", topic, err)
	}
}

func runRouted(t *testing.T, registry *Registry, id string, headers map[string]string, opts ...RouterOption) ResultInfo {
	t.Helper()
	pipeline := NewPipeline(RouterMiddleware(registry, opts...))
	ic := NewInvocationContext(NewTopic(id), headers, helloRequest{Name: "World"}, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return ic.Result
}

func wantMessage(t *testing.T, result ResultInfo, want string) {
	t.Helper()
	payload, ok := result.ResultPayload().(helloResponse)
	if !ok || payload.Message != want {
		t.Errorf("routed handler message = %v, want %q", result.ResultPayload(), want)
	}
}

func TestRouterMiddleware_ReadsVersionHeaderAndDispatchesExact(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("hello:world"), "unversioned")
	versionEcho(t, registry, NewTopic("hello:world").WithVersion("2"), "v2")

	result := runRouted(t, registry, "hello:world", map[string]string{"benzene-version": "2"})
	wantMessage(t, result, "v2:World") // the exact versioned handler ran, not the unversioned one
}

func TestRouterMiddleware_AbsentVersionRoutesToUnversioned(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("hello:world"), "unversioned")
	versionEcho(t, registry, NewTopic("hello:world").WithVersion("2"), "v2")

	result := runRouted(t, registry, "hello:world", nil)
	wantMessage(t, result, "unversioned:World") // no version signalled -> the default (unversioned) handler
}

func TestRouterMiddleware_UnmatchedVersionFallsBackToUnversioned(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("hello:world"), "unversioned")

	// A service that registered ONLY unversioned handlers must still route a message carrying a
	// stray version header - turning on the read path stays non-regressive.
	result := runRouted(t, registry, "hello:world", map[string]string{"benzene-version": "9"})
	wantMessage(t, result, "unversioned:World")
}

func TestRouterMiddleware_UnmatchedVersionWithoutUnversionedIsNotFound(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("hello:world").WithVersion("1"), "v1")

	// Only a versioned handler exists and the requested version doesn't match it, so there is no
	// default handler to fall back to: a genuinely unsupported version is rejected, not served.
	result := runRouted(t, registry, "hello:world", map[string]string{"benzene-version": "9"})
	if result.ResultStatus() != StatusNotFound {
		t.Errorf("ResultStatus() = %q, want %q", result.ResultStatus(), StatusNotFound)
	}
}

func TestRouterMiddleware_VersionFallbackListPrecedence(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("t").WithVersion("a"), "a")
	versionEcho(t, registry, NewTopic("t").WithVersion("b"), "b")
	versionEcho(t, registry, NewTopic("t").WithVersion("c"), "c")

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"benzene-version wins over version and x-version", map[string]string{"benzene-version": "a", "version": "b", "x-version": "c"}, "a:World"},
		{"version used when benzene-version absent", map[string]string{"version": "b", "x-version": "c"}, "b:World"},
		{"x-version used when the earlier names are absent", map[string]string{"x-version": "c"}, "c:World"},
		{"header match is case-insensitive", map[string]string{"Benzene-Version": "a"}, "a:World"},
		{"a present-but-empty name is skipped and the fallback continues", map[string]string{"benzene-version": "", "version": "b"}, "b:World"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantMessage(t, runRouted(t, registry, "t", tt.headers), tt.want)
		})
	}
}

func TestRouterMiddleware_BindingResolvedVersionIsNotOverriddenByHeader(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("t").WithVersion("route"), "route")
	versionEcho(t, registry, NewTopic("t").WithVersion("header"), "header")

	// A binding that resolved a version another way (e.g. an HTTP /v{version} route segment)
	// sets ic.Topic.Version before the pipeline runs; the router must not overwrite it with a
	// header. The topic already carries version "route", so a "header" version header is ignored.
	pipeline := NewPipeline(RouterMiddleware(registry))
	ic := NewInvocationContext(NewTopic("t").WithVersion("route"), map[string]string{"benzene-version": "header"}, helloRequest{Name: "World"}, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantMessage(t, ic.Result, "route:World")
}

func TestRouterMiddleware_WithVersionKeysNarrowsTheList(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("orders:create"), "unversioned")
	versionEcho(t, registry, NewTopic("orders:create").WithVersion("2"), "v2")

	// The producer emits a "version" header meaning its own API/client version (2), unrelated to
	// the payload schema version. Narrowing the list to "benzene-version" makes the router ignore
	// it, so the message routes to the unversioned handler rather than being misread as v2.
	result := runRouted(t, registry, "orders:create", map[string]string{"version": "2"}, WithVersionKeys("benzene-version"))
	wantMessage(t, result, "unversioned:World")

	// Without the narrowing, the default list DOES read "version" and dispatches the v2 handler.
	dflt := runRouted(t, registry, "orders:create", map[string]string{"version": "2"})
	wantMessage(t, dflt, "v2:World")
}
