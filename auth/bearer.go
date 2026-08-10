package auth

import (
	"context"
	"sort"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
)

const bearerScheme = "Bearer "

// bearerConfig holds the resolved BearerAuth knobs.
type bearerConfig struct {
	subjectClaim string
	rolesClaim   string
	onError      func(error)
}

// BearerOption configures BearerAuth.
type BearerOption func(*bearerConfig)

// WithSubjectClaim sets the claim mapped to Principal.Name. Default "sub" (the standard JWT subject).
func WithSubjectClaim(name string) BearerOption {
	return func(c *bearerConfig) { c.subjectClaim = name }
}

// WithRolesClaim sets the claim read into Principal.Roles. The claim may be a JSON array of strings
// or a single space-delimited string. Default "roles".
func WithRolesClaim(name string) BearerOption {
	return func(c *bearerConfig) { c.rolesClaim = name }
}

// WithOnError registers a hook called with the real validation error whenever a token is rejected.
// It exists so a service can log the true reason (bad signature vs expired vs wrong audience) for
// its own diagnostics WITHOUT that reason ever reaching the caller - BearerAuth always returns the
// same generic unauthorized result, so a token is never an oracle for an attacker (RFC 8725; matches
// the .NET middleware logging the reason server-side only). No forced logger dependency.
func WithOnError(hook func(error)) BearerOption {
	return func(c *bearerConfig) { c.onError = hook }
}

// BearerAuth returns an OAuth2/JWT bearer-token authentication middleware, the Go counterpart of
// Benzene.Auth.OAuth2's OAuth2BearerMiddleware. It reads `Authorization: Bearer <token>` from the
// message headers, validates the JWT with v (signature against the resolved key, the algorithm
// allowlist, and iss/aud/exp/nbf/iat - see Validator), and either sets the authenticated Principal
// on the context and calls next, or short-circuits with an unauthorized result carrying a
// `WWW-Authenticate: Bearer` challenge.
//
// Every rejection - missing header, malformed token, bad signature, expired, wrong audience - yields
// the SAME generic "invalid bearer token" result, so the caller learns nothing that would help forge
// a token; use WithOnError to surface the real reason to your own logs. Register it before the router
// and before any Authorize/RequireRole/RequireScope middleware.
//
// It reads the credential from the message headers, so like BasicAuth it is for HTTP-fronted
// pipelines; on a transport with no Authorization header it simply challenges every message.
func BearerAuth(v *Validator, opts ...BearerOption) benzene.Middleware {
	cfg := bearerConfig{subjectClaim: "sub", rolesClaim: "roles"}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		header := authorizationHeader(ic.Headers)
		if len(header) < len(bearerScheme) || !strings.EqualFold(header[:len(bearerScheme)], bearerScheme) {
			return bearerChallenge(ic, cfg, ErrMalformedToken)
		}
		token := strings.TrimSpace(header[len(bearerScheme):])
		if token == "" {
			return bearerChallenge(ic, cfg, ErrMalformedToken)
		}

		claims, err := v.Validate(ctx, token)
		if err != nil {
			return bearerChallenge(ic, cfg, err)
		}

		return next(ContextWithPrincipal(ctx, principalFromClaims(claims, cfg)))
	}
}

// bearerChallenge short-circuits the message with a generic unauthorized result and a
// `WWW-Authenticate: Bearer` header, calling the OnError hook (if any) with the real cause.
func bearerChallenge(ic *benzene.InvocationContext, cfg bearerConfig, cause error) error {
	if cfg.onError != nil {
		cfg.onError(cause)
	}
	ic.SetResponseHeader("WWW-Authenticate", "Bearer")
	ic.Result = benzene.Unauthorized[any]("invalid bearer token")
	return nil
}

// principalFromClaims maps validated JWT claims onto a Principal: the subject claim to Name, the
// roles claim to Roles, every top-level string claim into Claims, and the normalized granted scopes
// (see GrantedScopes) joined into the "scope" claim so RequireScope has one place to read.
func principalFromClaims(claims Claims, cfg bearerConfig) Principal {
	p := Principal{Claims: map[string]string{}}
	p.Name, _ = claims.String(cfg.subjectClaim)
	p.Roles = stringOrArray(claims[cfg.rolesClaim])

	for name, value := range claims {
		if s, ok := value.(string); ok {
			p.Claims[name] = s
		}
	}

	scopes := GrantedScopes(claims)
	if len(scopes) > 0 {
		p.Claims["scope"] = strings.Join(scopes, " ")
	}
	return p
}

// stringOrArray normalizes a claim that may be a JSON array of strings or a single space-delimited
// string into a slice - the two shapes a roles (or scope) claim appears in across issuers.
func stringOrArray(value any) []string {
	switch v := value.(type) {
	case string:
		return strings.Fields(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// GrantedScopes returns the flat, sorted set of OAuth2 scopes a token grants, merging the two
// conventions real issuers use (RFC 8693's space-delimited "scope" claim and Azure AD's "scp" claim,
// itself either space-delimited or a JSON array) - the Go counterpart of the .NET package's
// ScopeClaims. It is exported so a service can make scope-based decisions in a handler directly.
func GrantedScopes(claims Claims) []string {
	set := map[string]struct{}{}
	for _, s := range stringOrArray(claims["scope"]) {
		set[s] = struct{}{}
	}
	for _, s := range stringOrArray(claims["scp"]) {
		set[s] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out) // deterministic order (map iteration is randomized)
	return out
}

// RequireScope returns an authorization middleware that permits only a caller whose token granted
// scope - the scope-based counterpart of RequireRole, and sugar over Authorize reading the "scope"
// claim BearerAuth normalized onto the Principal. forbidden when the principal lacks the scope,
// unauthorized when there is no principal.
func RequireScope(scope string) benzene.Middleware {
	return Authorize(func(p Principal) bool {
		granted, _ := p.Claim("scope")
		for _, s := range strings.Fields(granted) {
			if s == scope {
				return true
			}
		}
		return false
	})
}
