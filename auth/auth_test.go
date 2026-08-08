package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestPrincipal_HasRoleAndClaim(t *testing.T) {
	p := Principal{Name: "alice", Roles: []string{"admin", "user"}, Claims: map[string]string{"tenant": "acme"}}
	if !p.HasRole("admin") || p.HasRole("superadmin") {
		t.Error("HasRole is wrong")
	}
	if v, ok := p.Claim("tenant"); !ok || v != "acme" {
		t.Errorf("Claim(tenant) = %q,%v, want acme,true", v, ok)
	}
	if _, ok := p.Claim("missing"); ok {
		t.Error("Claim(missing) should be absent")
	}
}

func TestContextPrincipalRoundTrip(t *testing.T) {
	ctx := ContextWithPrincipal(context.Background(), Principal{Name: "bob"})
	got, ok := PrincipalFromContext(ctx)
	if !ok || got.Name != "bob" {
		t.Errorf("PrincipalFromContext = %+v,%v, want bob", got, ok)
	}
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Error("PrincipalFromContext on a bare context should be absent")
	}
}

// runner wires an auth-middleware pipeline in front of a router with a handler that records whether
// it ran and what principal it saw.
type runner struct {
	pipeline   *benzene.Pipeline
	ran        bool
	sawName    string
	respHeader map[string]string
}

func newRunner(mw ...benzene.Middleware) *runner {
	r := &runner{}
	registry := benzene.NewRegistry()
	_ = benzene.Register(registry, benzene.NewTopic("secure"), benzene.Handler[map[string]any, struct{}](
		func(ctx context.Context, _ map[string]any) benzene.Result[struct{}] {
			r.ran = true
			if p, ok := PrincipalFromContext(ctx); ok {
				r.sawName = p.Name
			}
			return benzene.Ok(struct{}{})
		}))
	r.pipeline = benzene.NewPipeline(append(mw, benzene.RouterMiddleware(registry))...)
	return r
}

func (r *runner) send(t *testing.T, headers map[string]string) *benzene.InvocationContext {
	t.Helper()
	ic := benzene.NewInvocationContext(benzene.NewTopic("secure"), headers, json.RawMessage(`{}`), benzene.NewContainer().NewScope())
	if err := r.pipeline.Run(context.Background(), ic); err != nil {
		t.Fatalf("pipeline.Run() error = %v", err)
	}
	r.respHeader = ic.ResponseHeaders
	return ic
}

func basicHeader(user, pass string) map[string]string {
	return map[string]string{"authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))}
}

// validator accepts alice/secret as an admin.
func validator(username, password string) (Principal, bool) {
	if username == "alice" && password == "secret" {
		return Principal{Name: "alice", Roles: []string{"admin"}}, true
	}
	return Principal{}, false
}

func TestBasicAuth_ValidCredentialsSetPrincipalAndRun(t *testing.T) {
	r := newRunner(BasicAuth(validator, "test"))
	ic := r.send(t, basicHeader("alice", "secret"))

	if !r.ran {
		t.Fatal("handler did not run with valid credentials")
	}
	if r.sawName != "alice" {
		t.Errorf("handler saw principal %q, want alice", r.sawName)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok", ic.Result.ResultStatus())
	}
}

func TestBasicAuth_InvalidCredentialsChallenge(t *testing.T) {
	r := newRunner(BasicAuth(validator, "test"))
	ic := r.send(t, basicHeader("alice", "wrong"))

	if r.ran {
		t.Error("handler ran despite invalid credentials")
	}
	if ic.Result.ResultStatus() != benzene.StatusUnauthorized {
		t.Errorf("status = %q, want unauthorized", ic.Result.ResultStatus())
	}
	// SetResponseHeader lowercases the header name (HTTP header names are case-insensitive).
	if r.respHeader["www-authenticate"] != `Basic realm="test"` {
		t.Errorf("www-authenticate = %q, want the Basic challenge", r.respHeader["www-authenticate"])
	}
}

func TestBasicAuth_MalformedHeaders(t *testing.T) {
	tests := map[string]map[string]string{
		"missing":            {},
		"not basic":          {"authorization": "Bearer xyz"},
		"not base64":         {"authorization": "Basic !!!not-base64!!!"},
		"no colon separator": {"authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon"))},
	}
	for name, headers := range tests {
		t.Run(name, func(t *testing.T) {
			r := newRunner(BasicAuth(validator, "test"))
			ic := r.send(t, headers)
			if r.ran {
				t.Error("handler ran for a malformed Authorization header")
			}
			if ic.Result.ResultStatus() != benzene.StatusUnauthorized {
				t.Errorf("status = %q, want unauthorized", ic.Result.ResultStatus())
			}
		})
	}
}

func TestBasicAuth_PasswordMayContainColon(t *testing.T) {
	v := func(username, password string) (Principal, bool) {
		if username == "u" && password == "p:a:s:s" {
			return Principal{Name: "u"}, true
		}
		return Principal{}, false
	}
	r := newRunner(BasicAuth(v, "test"))
	r.send(t, basicHeader("u", "p:a:s:s"))
	if !r.ran {
		t.Error("a password containing ':' was not handled (RFC 7617 allows it)")
	}
}

func TestBasicAuth_HeaderNameIsCaseInsensitive(t *testing.T) {
	r := newRunner(BasicAuth(validator, "test"))
	r.send(t, map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))})
	if !r.ran {
		t.Error("a mixed-case Authorization header was not read")
	}
}

func TestRequireRole_PermitsAndForbids(t *testing.T) {
	// alice is an admin -> RequireRole("admin") permits; RequireRole("superadmin") forbids.
	permit := newRunner(BasicAuth(validator, "test"), RequireRole("admin"))
	if ic := permit.send(t, basicHeader("alice", "secret")); ic.Result.ResultStatus() != benzene.StatusOk || !permit.ran {
		t.Errorf("RequireRole(admin) should permit alice; status = %q ran = %v", ic.Result.ResultStatus(), permit.ran)
	}

	forbid := newRunner(BasicAuth(validator, "test"), RequireRole("superadmin"))
	ic := forbid.send(t, basicHeader("alice", "secret"))
	if forbid.ran {
		t.Error("handler ran despite the caller lacking the required role")
	}
	if ic.Result.ResultStatus() != benzene.StatusForbidden {
		t.Errorf("status = %q, want forbidden", ic.Result.ResultStatus())
	}
}

func TestAuthorize_NoPrincipalIsUnauthorized(t *testing.T) {
	// Authorize without an authentication middleware in front -> no principal -> unauthorized.
	r := newRunner(Authorize(func(Principal) bool { return true }))
	ic := r.send(t, nil)
	if r.ran {
		t.Error("handler ran with no authenticated principal")
	}
	if ic.Result.ResultStatus() != benzene.StatusUnauthorized {
		t.Errorf("status = %q, want unauthorized", ic.Result.ResultStatus())
	}
}
