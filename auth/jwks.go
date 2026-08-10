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

	mu        sync.Mutex
	byKid     map[string][]VerificationKey
	keyless   []VerificationKey
	fetched   bool
	lastFetch time.Time
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
	jwksURL, err := tmp.discoverJWKSURL(ctx, discoveryURL)
	if err != nil {
		return nil, err
	}
	return NewJWKSResolver(jwksURL, opts...)
}

// discoverJWKSURL fetches the OIDC discovery document and returns its jwks_uri.
func (r *JWKSResolver) discoverJWKSURL(ctx context.Context, discoveryURL string) (string, error) {
	body, err := r.get(ctx, discoveryURL)
	if err != nil {
		return "", err
	}
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("jwt: OIDC discovery document is not valid JSON: %w", err)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("jwt: OIDC discovery document has no jwks_uri")
	}
	return doc.JWKSURI, nil
}

// ResolveKeys returns the cached keys for kid (plus keyless keys), fetching the JWKS on first use
// and refetching once - throttled by the min-refetch window - when kid is unknown, to pick up a
// rotated key. It holds a lock across the network fetch so concurrent unknown-kid misses collapse to
// a single refetch instead of a stampede; fetches are rare (cache hit is the norm), so the brief
// serialization is acceptable.
func (r *JWKSResolver) ResolveKeys(ctx context.Context, kid string) ([]VerificationKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.fetched {
		if err := r.fetch(ctx); err != nil {
			return nil, err
		}
	}

	if keys := r.lookupLocked(kid); len(keys) > 0 {
		return keys, nil
	}

	// Unknown kid: the signing key may have rotated. Refetch once if the throttle window has passed.
	if kid != "" && r.now().Sub(r.lastFetch) >= r.minRefetch {
		if err := r.fetch(ctx); err != nil {
			return nil, err
		}
		return r.lookupLocked(kid), nil
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
	r.lastFetch = r.now()
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
