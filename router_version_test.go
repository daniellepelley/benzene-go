package benzene

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// versionEcho registers a handler for topic that answers with a message naming which handler
// ran, so a test can tell which handler version selection picked.
func versionEcho(t *testing.T, registry *Registry, topic Topic, label string, opts ...RegisterOption) {
	t.Helper()
	h := Handler[helloRequest, helloResponse](func(_ context.Context, req helloRequest) Result[helloResponse] {
		return Ok(helloResponse{Message: label + ":" + req.Name})
	})
	if err := Register(registry, topic, h, opts...); err != nil {
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

func TestRouterMiddleware_UnknownVersionIsNotFoundEvenWithAnUnversionedHandler(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("hello:world"), "unversioned")

	// core-concepts.md §2: a caller that named a version gets that version or not-found - never
	// another version's behaviour, the unversioned handler's included.
	result := runRouted(t, registry, "hello:world", map[string]string{"benzene-version": "9"})
	wantNotFound(t, result, `"hello:world"`, `"9"`)
	if IsUnmatchedTopic(result) {
		t.Error("IsUnmatchedTopic() = true for a version the topic does not serve, want false: the topic is this service's own")
	}
}

func TestRouterMiddleware_UnknownVersionWithoutUnversionedIsNotFound(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("hello:world").WithVersion("1"), "v1")

	result := runRouted(t, registry, "hello:world", map[string]string{"benzene-version": "9"})
	wantNotFound(t, result, `"hello:world"`, `"9"`)
}

func TestRouterMiddleware_NoHighestAvailableFallback(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("t").WithVersion("1"), "v1", AsDefault())
	versionEcho(t, registry, NewTopic("t").WithVersion("2"), "v2")

	// "3" is newer than anything registered; serving v2 (or the v1 default) would be the
	// highest-available fallback core-concepts.md §2 rules out.
	wantNotFound(t, runRouted(t, registry, "t", map[string]string{"benzene-version": "3"}), `"t"`, `"3"`)
}

func TestRouterMiddleware_AbsentVersionRoutesToDeclaredDefault(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("t").WithVersion("v1"), "v1", AsDefault())
	versionEcho(t, registry, NewTopic("t").WithVersion("v2"), "v2")

	wantMessage(t, runRouted(t, registry, "t", nil), "v1:World")
	wantMessage(t, runRouted(t, registry, "t", map[string]string{"benzene-version": "v2"}), "v2:World")
}

func TestRouterMiddleware_UnversionedHandlerWinsOverDeclaredDefault(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("t").WithVersion("v1"), "v1", AsDefault())
	versionEcho(t, registry, NewTopic("t"), "unversioned")

	// core-concepts.md §2: the unversioned handler is the default if one is registered; a declared
	// default is used only otherwise.
	wantMessage(t, runRouted(t, registry, "t", nil), "unversioned:World")
}

func TestRouterMiddleware_AbsentVersionWithoutDefaultIsNotFound(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("t").WithVersion("v1"), "v1")
	versionEcho(t, registry, NewTopic("t").WithVersion("v2"), "v2")

	result := runRouted(t, registry, "t", nil)
	wantNotFound(t, result, `"t"`)
	if IsUnmatchedTopic(result) {
		t.Error("IsUnmatchedTopic() = true for a topic the service serves, want false")
	}
}

func TestRouterMiddleware_WarnsAtStartupForATopicWithoutDefault(t *testing.T) {
	var logged strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("orders:create").WithVersion("v1"), "v1")
	versionEcho(t, registry, NewTopic("orders:ship").WithVersion("v1"), "v1", AsDefault())
	versionEcho(t, registry, NewTopic("orders:list"), "unversioned")

	RouterMiddleware(registry)

	out := logged.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "topic=orders:create") {
		t.Errorf("start-up log = %q, want a warning naming orders:create", out)
	}
	for _, id := range []string{"orders:ship", "orders:list"} {
		if strings.Contains(out, "topic="+id) {
			t.Errorf("start-up log names %s, which has a default handler: %q", id, out)
		}
	}
}

func TestRouterMiddleware_PlainVersionHeaderSelectsNothing(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("t").WithVersion("a"), "a", AsDefault())
	versionEcho(t, registry, NewTopic("t").WithVersion("b"), "b")

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"benzene-version selects", map[string]string{"benzene-version": "b"}, "b:World"},
		{"header match is case-insensitive", map[string]string{"Benzene-Version": "b"}, "b:World"},
		// D10: one name, no fallback list - a producer's own version header goes to the default.
		{"a bare version header is an ordinary header", map[string]string{"version": "b"}, "a:World"},
		{"an x-version header is an ordinary header", map[string]string{"x-version": "b"}, "a:World"},
		{"a present-but-empty benzene-version is absent", map[string]string{"benzene-version": ""}, "a:World"},
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

func TestRouterMiddleware_WithVersionKeyRenamesTheHeader(t *testing.T) {
	registry := NewRegistry()
	versionEcho(t, registry, NewTopic("orders:create"), "unversioned")
	versionEcho(t, registry, NewTopic("orders:create").WithVersion("2"), "v2")

	// A service whose producers send the version as "schema-version" renames the one key; the
	// configured name replaces benzene-version rather than joining it.
	wantMessage(t, runRouted(t, registry, "orders:create", map[string]string{"schema-version": "2"}, WithVersionKey("schema-version")), "v2:World")
	wantMessage(t, runRouted(t, registry, "orders:create", map[string]string{"benzene-version": "2"}, WithVersionKey("schema-version")), "unversioned:World")
}

func TestRouterMiddleware_AnswersPing(t *testing.T) {
	registry := NewRegistry()

	for _, headers := range []map[string]string{nil, {"benzene-version": "v7"}} {
		result := runRouted(t, registry, PingTopic, headers)
		if result.ResultStatus() != StatusOk {
			t.Errorf("ping with headers %v: status = %q, want ok", headers, result.ResultStatus())
		}
		if result.ResultPayload() != nil {
			t.Errorf("ping with headers %v: payload = %v, want none", headers, result.ResultPayload())
		}
	}
}

func TestRouterMiddleware_UnmatchedTopicIsMarked(t *testing.T) {
	registry := NewRegistry()
	result := runRouted(t, registry, "someone:elses", nil)
	wantNotFound(t, result, `"someone:elses"`)
	if !IsUnmatchedTopic(result) {
		t.Error("IsUnmatchedTopic() = false for a topic no handler serves, want true")
	}

	// A handler's own not-found is not an unmatched topic.
	h := Handler[helloRequest, helloResponse](func(context.Context, helloRequest) Result[helloResponse] {
		return NotFound[helloResponse]("no such order")
	})
	MustRegister(registry, NewTopic("orders:get"), h)
	if IsUnmatchedTopic(runRouted(t, registry, "orders:get", nil)) {
		t.Error("IsUnmatchedTopic() = true for a handler's own not-found, want false")
	}
}

// wantNotFound asserts result is not-found and its error names each of the given fragments.
func wantNotFound(t *testing.T, result ResultInfo, fragments ...string) {
	t.Helper()
	if result.ResultStatus() != StatusNotFound {
		t.Fatalf("ResultStatus() = %q, want %q", result.ResultStatus(), StatusNotFound)
	}
	message := strings.Join(result.ResultErrors(), "; ")
	for _, fragment := range fragments {
		if !strings.Contains(message, fragment) {
			t.Errorf("not-found error %q does not name %s", message, fragment)
		}
	}
}
