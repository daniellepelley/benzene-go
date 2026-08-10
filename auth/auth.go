// Package auth is the authentication/authorization building block, matching Benzene.Auth.Core (+
// Benzene.Auth.Basic and Benzene.Auth.OAuth2). It is zero-dependency: a Principal is a small value on
// the invocation context, authentication is a middleware that sets it, and authorization is a
// middleware that short-circuits with unauthorized/forbidden when the caller isn't permitted - the
// framework owns the enforcement mechanism, the application owns what a credential and a policy mean.
//
// Go has no ClaimsPrincipal, so Principal here is a plain struct (name + roles + claims). The
// authenticated principal rides on the context (ContextWithPrincipal), set by an authentication
// middleware and read downstream by authorization middleware and by handlers (PrincipalFromContext).
//
// Two authentication middlewares ship, both reading a credential from the message headers - so both
// are for HTTP-fronted pipelines, where the transport carries an Authorization header:
//
//   - BasicAuth (basic.go) - RFC 7617 Basic auth, validated by an app-supplied credential check.
//   - BearerAuth (bearer.go) - OAuth2/JWT bearer tokens, validated by a Validator (jwt.go) against a
//     signing-algorithm allowlist and iss/aud/exp checks, with keys from StaticKeys or a JWKS
//     resolver. The security-critical JWT validation is pure standard library, so this stays
//     zero-dependency (where the .NET Benzene.Auth.OAuth2 leans on Microsoft.IdentityModel).
//
// Each sets the principal and calls next, or short-circuits with an unauthorized result. Authorization
// middleware (Authorize / RequireRole / RequireScope) runs after authentication and gates the handler
// on the principal.
package auth

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// Principal is the authenticated caller for a message: an identity, its roles, and arbitrary
// string claims. An authentication middleware constructs it (e.g. BasicAuth via its validator) and
// puts it on the context; handlers and authorization middleware read it back.
type Principal struct {
	// Name identifies the caller (a username, a service account, a subject claim).
	Name string
	// Roles the caller holds, for role-based authorization (see RequireRole).
	Roles []string
	// Claims are arbitrary name/value assertions about the caller.
	Claims map[string]string
}

// HasRole reports whether the principal holds role (exact, case-sensitive match).
func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Claim returns the value of the named claim and whether it was present.
func (p Principal) Claim(name string) (string, bool) {
	value, ok := p.Claims[name]
	return value, ok
}

type principalKey struct{}

// ContextWithPrincipal returns a copy of ctx carrying principal, so downstream authorization
// middleware and the handler can read it with PrincipalFromContext. An authentication middleware
// calls next with this augmented context.
func ContextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFromContext returns the authenticated principal an authentication middleware set on the
// context. ok is false when no authentication middleware ran (or it short-circuited before setting
// one) - authorization middleware treats that as unauthenticated.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

// Authorize returns an authorization middleware that runs authorized against the authenticated
// principal and short-circuits when the caller is not permitted: an unauthorized result when there
// is no principal on the context (no authentication ran, or it failed), a forbidden result when
// there is a principal but authorized returns false. Register it after an authentication middleware
// and before the router. This is the general primitive; RequireRole is the common convenience over
// it, and the Go counterpart to Benzene.Auth.Core's IAuthorizationPolicy.
func Authorize(authorized func(Principal) bool) benzene.Middleware {
	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		principal, ok := PrincipalFromContext(ctx)
		if !ok {
			ic.Result = benzene.Unauthorized[any]("not authenticated")
			return nil // short-circuit: the router/handler downstream do not run
		}
		if !authorized(principal) {
			ic.Result = benzene.Forbidden[any]("caller is not authorized")
			return nil
		}
		return next(ctx)
	}
}

// RequireRole returns an authorization middleware that permits only a caller holding role, and is
// sugar for Authorize(func(p) bool { return p.HasRole(role) }).
func RequireRole(role string) benzene.Middleware {
	return Authorize(func(principal Principal) bool { return principal.HasRole(role) })
}
