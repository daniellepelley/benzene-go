package auth

import (
	"context"
	"encoding/base64"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
)

// BasicValidator validates an RFC 7617 Basic-auth username/password pair and returns the
// authenticated Principal on success. ok is false for wrong credentials - that is an ordinary
// unauthorized outcome, not an error, so a validator never signals "wrong password" any other way.
// Implement it against whatever credential store the service actually uses; this package ships no
// default so there is no hardcoded-credential footgun to deploy by accident.
type BasicValidator func(username, password string) (Principal, bool)

const basicScheme = "Basic "

// BasicAuth returns an RFC 7617 HTTP Basic authentication middleware: it reads the
// `Authorization: Basic <base64>` header from the message, validates the decoded username/password
// with validate, and either sets the authenticated Principal on the context and calls next, or
// short-circuits with an unauthorized result carrying a `WWW-Authenticate: Basic realm="<realm>"`
// challenge (which is what makes an HTTP client prompt for credentials). Register it before the
// router and before any Authorize/RequireRole middleware.
//
// It reads the credential from the message headers, so it is for HTTP-fronted pipelines (where the
// transport carries an Authorization header); on a queue-shaped transport with no such header it
// simply challenges every message.
func BasicAuth(validate BasicValidator, realm string) benzene.Middleware {
	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		header := authorizationHeader(ic.Headers)
		if len(header) < len(basicScheme) || !strings.EqualFold(header[:len(basicScheme)], basicScheme) {
			return challenge(ic, realm, "missing or malformed Authorization header")
		}

		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(basicScheme):]))
		if err != nil {
			return challenge(ic, realm, "malformed Basic credentials")
		}

		// Split on the FIRST ':' only - RFC 7617 allows ':' inside the password, so splitting on
		// every ':' would misassign it.
		username, password, ok := strings.Cut(string(decoded), ":")
		if !ok {
			return challenge(ic, realm, "malformed Basic credentials")
		}

		principal, ok := validate(username, password)
		if !ok {
			return challenge(ic, realm, "invalid credentials")
		}

		return next(ContextWithPrincipal(ctx, principal))
	}
}

// challenge short-circuits the message with an unauthorized result and the RFC 7617
// WWW-Authenticate response header (set on every unauthorized outcome, not just a missing header,
// per RFC 7617).
func challenge(ic *benzene.InvocationContext, realm, detail string) error {
	ic.SetResponseHeader("WWW-Authenticate", `Basic realm="`+realm+`"`)
	ic.Result = benzene.Unauthorized[any](detail)
	return nil
}

// authorizationHeader looks the Authorization header up exactly first (deterministic), then
// case-insensitively; HTTP bindings normalize header names, so the fallback is only a convenience.
func authorizationHeader(headers map[string]string) string {
	if value, ok := headers["authorization"]; ok {
		return value
	}
	for name, value := range headers {
		if strings.EqualFold(name, "authorization") {
			return value
		}
	}
	return ""
}
