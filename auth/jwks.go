package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// JWKSResolver is a KeyResolver that fetches an identity provider's JSON Web Key Set (RFC 7517) over
// HTTP and caches it, matching the role Microsoft.IdentityModel's ConfigurationManager plays in the
// .NET OAuth2 middleware. It refetches when a token presents a "kid" the cache doesn't hold (key
// rotation), throttled so a burst of tokens bearing an unknown kid can't hammer the JWKS endpoint.
// Zero-dependency: net/http + encoding/json + the crypto in jwtkeys.go.
type JWKSResolver struct {
	url          string
	client       *http.Client
	requireHTTPS bool
	minRefetch   time.Duration
	now          func() time.Time

	mu          sync.Mutex
	byKid       map[string][]VerificationKey
	keyless     []VerificationKey
	fetched     bool
	lastAttempt time.Time // stamped on every fetch attempt (success or failure) to throttle both
}

// JWKSOption configures a JWKSResolver.
type JWKSOption func(*JWKSResolver)

// WithHTTPClient sets the http.Client used to fetch the JWKS (and, for NewJWKSFromAuthority, the
// discovery document). Default: a client with a 10s timeout - always set a timeout in production so
// a hung JWKS endpoint can't stall authentication indefinitely.
func WithHTTPClient(client *http.Client) JWKSOption {
	return func(r *JWKSResolver) { r.client = client }
}

// WithRequireHTTPS controls whether the JWKS (and discovery) URL must be https. Default true:
// fetching the document that establishes signing trust over plain HTTP invites a man-in-the-middle
// swapping the signing key, so this stays required except for local testing against a plain-HTTP
// fake - the same escape hatch as .NET's RequireHttpsMetadata.
func WithRequireHTTPS(require bool) JWKSOption {
	return func(r *JWKSResolver) { r.requireHTTPS = require }
}

// WithMinRefetchInterval bounds how often an unknown-kid miss may trigger a refetch (default 5
// minutes), so token traffic bearing an unrecognized kid can't turn into a JWKS-endpoint flood.
func WithMinRefetchInterval(d time.Duration) JWKSOption {
	return func(r *JWKSResolver) { r.minRefetch = d }
}

// withJWKSClock overrides the clock (tests only), so the refetch-throttle window can be exercised
// without real time passing.
func withJWKSClock(now func() time.Time) JWKSOption {
	return func(r *JWKSResolver) { r.now = now }
}

// NewJWKSResolver builds a resolver for a known JWKS document URL. The document is fetched lazily on
// the first ResolveKeys call, then cached. Returns an error only for an https-required violation of
// the given URL; network/parse failures surface later, per fetch.
func NewJWKSResolver(jwksURL string, opts ...JWKSOption) (*JWKSResolver, error) {
	r := &JWKSResolver{
		url:          jwksURL,
		client:       &http.Client{Timeout: 10 * time.Second},
		requireHTTPS: true,
		minRefetch:   5 * time.Minute,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(r)
	}
	if err := r.checkScheme(jwksURL); err != nil {
		return nil, err
	}
	return r, nil
}

// NewJWKSFromAuthority discovers the JWKS URL from an OIDC issuer's discovery document
// (<authority>/.well-known/openid-configuration, RFC 8414 / OpenID Connect Discovery) and returns a
// resolver for the jwks_uri it advertises - the path most identity providers (Auth0, Cognito, Azure
// AD, Okta) support. The discovery fetch happens here (once); the JWKS itself is then fetched lazily.
func NewJWKSFromAuthority(ctx context.Context, authority string, opts ...JWKSOption) (*JWKSResolver, error) {
	// Build a throwaway resolver just to resolve the configured client / https policy for the
	// discovery fetch, then reuse them for the real resolver.
	tmp := &JWKSResolver{client: &http.Client{Timeout: 10 * time.Second}, requireHTTPS: true, minRefetch: 5 * time.Minute, now: time.Now}
	for _, opt := range opts {
		opt(tmp)
	}
	discoveryURL := strings.TrimRight(authority, "/") + "/.well-known/openid-configuration"
	if err := tmp.checkScheme(discoveryURL); err != nil {
		return nil, err
	}
	jwksURL, err := tmp.discoverJWKSURL(ctx, authority, discoveryURL)
	if err != nil {
		return nil, err
	}
	return NewJWKSResolver(jwksURL, opts...)
}

// discoverJWKSURL fetches the OIDC discovery document and returns its jwks_uri, after verifying the
// document's issuer matches the configured authority (RFC 8414 §3 requires this - it stops a rogue
// discovery endpoint from pointing this service at an attacker-controlled JWKS under a different
// issuer's name).
func (r *JWKSResolver) discoverJWKSURL(ctx context.Context, authority, discoveryURL string) (string, error) {
	body, err := r.get(ctx, discoveryURL)
	if err != nil {
		return "", err
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("jwt: OIDC discovery document is not valid JSON: %w", err)
	}
	if strings.TrimRight(doc.Issuer, "/") != strings.TrimRight(authority, "/") {
		return "", fmt.Errorf("jwt: OIDC discovery issuer %q does not match the configured authority %q", doc.Issuer, authority)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("jwt: OIDC discovery document has no jwks_uri")
	}
	return doc.JWKSURI, nil
}

// ResolveKeys returns the cached keys for kid (plus keyless keys), fetching the JWKS on first use and
// refetching - to pick up a rotated key - when a token presents a kid the cache doesn't hold.
//
// Both the initial fetch and the unknown-kid refetch are gated by the same min-refetch window, and
// the window is stamped on every ATTEMPT (success OR failure). That is the load-bearing detail: a
// down or slow identity provider - or a flood of tokens bearing random unknown kids - can therefore
// trigger at most one fetch per window, never one fetch per request, so it can't be turned into a
// DoS against the JWKS endpoint (or against this service). A refetch that fails keeps serving the
// existing (stale) cache rather than failing every request while the IdP is briefly unavailable;
// only a failed INITIAL fetch (no cache to fall back on) surfaces its error.
//
// The lock is held across the fetch so concurrent misses collapse to a single fetch instead of a
// stampede. Because the throttle bounds a fetch to at most once per window, the interval during
// which concurrent callers can block on that lock is correspondingly rare; keep the http.Client
// timeout short so even that rare fetch can't stall authentication for long.
func (r *JWKSResolver) ResolveKeys(ctx context.Context, kid string) ([]VerificationKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Fast path: a cached key already satisfies this token - no fetch, no throttle interaction.
	if r.fetched {
		if keys := r.lookupLocked(kid); len(keys) > 0 {
			return keys, nil
		}
	}

	// (Re)fetch when we have no cache yet, or the token carries a specific kid we don't hold - but
	// only once per throttle window, stamped on the attempt regardless of outcome.
	if (!r.fetched || kid != "") && r.now().Sub(r.lastAttempt) >= r.minRefetch {
		r.lastAttempt = r.now()
		if err := r.fetch(ctx); err != nil && !r.fetched {
			return nil, err // failed initial fetch: no stale cache to serve
		}
	}
	if !r.fetched {
		return nil, nil // throttled after a failed initial fetch; caller resolves this to ErrNoKey
	}
	return r.lookupLocked(kid), nil
}

// lookupLocked returns the keys for kid plus the keyless keys. Caller holds r.mu.
func (r *JWKSResolver) lookupLocked(kid string) []VerificationKey {
	var keys []VerificationKey
	if kid != "" {
		keys = append(keys, r.byKid[kid]...)
	}
	keys = append(keys, r.keyless...)
	return keys
}

// fetch retrieves and parses the JWKS, replacing the cache. Caller holds r.mu.
func (r *JWKSResolver) fetch(ctx context.Context) error {
	body, err := r.get(ctx, r.url)
	if err != nil {
		return err
	}
	byKid, keyless, err := parseJWKSet(body)
	if err != nil {
		return err
	}
	r.byKid = byKid
	r.keyless = keyless
	r.fetched = true
	return nil
}

// get performs a GET and returns the body, requiring a 2xx status. rawURL is assumed already
// validated by the caller (NewJWKSResolver / NewJWKSFromAuthority both checkScheme before any get),
// so the NewRequestWithContext error below is a defensive branch that a validated URL never reaches.
func (r *JWKSResolver) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jwt: bad JWKS URL %q: %w", rawURL, err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwt: fetching %q: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwt: fetching %q returned status %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jwt: reading %q: %w", rawURL, err)
	}
	return body, nil
}

// checkScheme enforces the https requirement (when enabled) and that the URL parses with a host.
func (r *JWKSResolver) checkScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("jwt: invalid URL %q", rawURL)
	}
	if r.requireHTTPS && u.Scheme != "https" {
		return fmt.Errorf("jwt: URL %q must be https (set WithRequireHTTPS(false) only for local testing)", rawURL)
	}
	return nil
}
