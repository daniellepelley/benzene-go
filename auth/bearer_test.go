package auth

import (
	"errors"
	"reflect"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

func bearerHeader(token string) map[string]string {
	return map[string]string{"authorization": "Bearer " + token}
}

func TestBearerAuth_ValidTokenSetsPrincipalAndRuns(t *testing.T) {
	v := fixedValidator(t, HS256, "")
	r := newRunner(BearerAuth(v))
	ic := r.send(t, bearerHeader(makeToken(t, HS256, "", goodClaims())))

	if !r.ran {
		t.Fatal("handler did not run for a valid token")
	}
	if r.sawName != "user-123" {
		t.Errorf("principal name = %q, want user-123 (from sub)", r.sawName)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok", ic.Result.ResultStatus())
	}
}

func TestBearerAuth_MissingOrMalformedHeaderChallenges(t *testing.T) {
	v := fixedValidator(t, HS256, "")
	cases := map[string]map[string]string{
		"no header":       nil,
		"wrong scheme":    {"authorization": "Basic abc"},
		"empty token":     {"authorization": "Bearer "},
		"only whitespace": {"authorization": "Bearer    "},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRunner(BearerAuth(v))
			ic := r.send(t, headers)
			if r.ran {
				t.Error("handler ran despite a missing/malformed bearer header")
			}
			if ic.Result.ResultStatus() != benzene.StatusUnauthorized {
				t.Errorf("status = %q, want unauthorized", ic.Result.ResultStatus())
			}
			if r.respHeader["www-authenticate"] != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", r.respHeader["www-authenticate"])
			}
		})
	}
}

func TestBearerAuth_InvalidTokenIsGenericAndCallsOnError(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	v := fixedValidator(t, HS256, "", WithClock(func() time.Time { return now }))

	var gotErr error
	r := newRunner(BearerAuth(v, WithOnError(func(err error) { gotErr = err })))

	claims := goodClaims()
	claims["exp"] = float64(now.Add(-time.Hour).Unix()) // expired
	ic := r.send(t, bearerHeader(makeToken(t, HS256, "", claims)))

	if r.ran {
		t.Error("handler ran for an expired token")
	}
	if ic.Result.ResultStatus() != benzene.StatusUnauthorized {
		t.Errorf("status = %q, want unauthorized", ic.Result.ResultStatus())
	}
	// The caller sees a GENERIC message; the real reason only reaches the OnError hook.
	if errs := ic.Result.ResultErrors(); len(errs) != 1 || errs[0] != "invalid bearer token" {
		t.Errorf("result errors = %v, want the generic [invalid bearer token]", errs)
	}
	if !errors.Is(gotErr, ErrTokenExpired) {
		t.Errorf("OnError got %v, want ErrTokenExpired", gotErr)
	}
}

func TestBearerAuth_CustomSubjectAndRolesClaims(t *testing.T) {
	v := fixedValidator(t, HS256, "")
	r := newRunner(BearerAuth(v, WithSubjectClaim("email"), WithRolesClaim("groups")))

	claims := goodClaims()
	claims["email"] = "alice@example.com"
	claims["groups"] = []any{"admins"}
	ic := r.send(t, bearerHeader(makeToken(t, HS256, "", claims)))

	if r.sawName != "alice@example.com" {
		t.Errorf("name = %q, want alice@example.com (from email claim)", r.sawName)
	}
	if ic.Result.ResultStatus() != benzene.StatusOk {
		t.Errorf("status = %q, want ok", ic.Result.ResultStatus())
	}
}

func TestPrincipalFromClaims_Mapping(t *testing.T) {
	claims := Claims{
		"sub":    "user-1",
		"roles":  []any{"admin", "user"},
		"tenant": "acme",
		"scope":  "read write",
		"count":  float64(3), // non-string claim: skipped from the string Claims map
	}
	p := principalFromClaims(claims, bearerConfig{subjectClaim: "sub", rolesClaim: "roles"})

	if p.Name != "user-1" {
		t.Errorf("Name = %q, want user-1", p.Name)
	}
	if !reflect.DeepEqual(p.Roles, []string{"admin", "user"}) {
		t.Errorf("Roles = %v, want [admin user]", p.Roles)
	}
	if v, _ := p.Claim("tenant"); v != "acme" {
		t.Errorf("tenant claim = %q, want acme", v)
	}
	if _, ok := p.Claim("count"); ok {
		t.Error("non-string claim count should not be in the string Claims map")
	}
	if v, _ := p.Claim("scope"); v != "read write" {
		t.Errorf("scope claim = %q, want 'read write'", v)
	}
}

func TestPrincipalFromClaims_RolesAsString(t *testing.T) {
	p := principalFromClaims(Claims{"sub": "u", "roles": "a b c"}, bearerConfig{subjectClaim: "sub", rolesClaim: "roles"})
	if !reflect.DeepEqual(p.Roles, []string{"a", "b", "c"}) {
		t.Errorf("Roles = %v, want [a b c] from a space-delimited string", p.Roles)
	}
}

func TestGrantedScopes(t *testing.T) {
	t.Run("merges scope and scp, dedupes, sorts", func(t *testing.T) {
		got := GrantedScopes(Claims{"scope": "read write", "scp": []any{"write", "admin"}})
		if !reflect.DeepEqual(got, []string{"admin", "read", "write"}) {
			t.Errorf("scopes = %v, want [admin read write]", got)
		}
	})
	t.Run("scp as space-delimited string", func(t *testing.T) {
		got := GrantedScopes(Claims{"scp": "a b"})
		if !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Errorf("scopes = %v, want [a b]", got)
		}
	})
	t.Run("none yields nil", func(t *testing.T) {
		if got := GrantedScopes(Claims{"sub": "u"}); got != nil {
			t.Errorf("scopes = %v, want nil", got)
		}
	})
}

func TestRequireScope(t *testing.T) {
	v := fixedValidator(t, HS256, "")
	tokenWithScopes := func(scope string) map[string]string {
		c := goodClaims()
		c["scope"] = scope
		return bearerHeader(makeToken(t, HS256, "", c))
	}

	t.Run("granted scope runs", func(t *testing.T) {
		r := newRunner(BearerAuth(v), RequireScope("read"))
		ic := r.send(t, tokenWithScopes("read write"))
		if !r.ran || ic.Result.ResultStatus() != benzene.StatusOk {
			t.Errorf("ran=%v status=%q, want ran + ok", r.ran, ic.Result.ResultStatus())
		}
	})
	t.Run("missing scope forbidden", func(t *testing.T) {
		r := newRunner(BearerAuth(v), RequireScope("admin"))
		ic := r.send(t, tokenWithScopes("read write"))
		if r.ran {
			t.Error("handler ran without the required scope")
		}
		if ic.Result.ResultStatus() != benzene.StatusForbidden {
			t.Errorf("status = %q, want forbidden", ic.Result.ResultStatus())
		}
	})
	t.Run("no principal unauthorized", func(t *testing.T) {
		r := newRunner(RequireScope("read")) // no BearerAuth ahead of it
		ic := r.send(t, nil)
		if ic.Result.ResultStatus() != benzene.StatusUnauthorized {
			t.Errorf("status = %q, want unauthorized", ic.Result.ResultStatus())
		}
	})
}
