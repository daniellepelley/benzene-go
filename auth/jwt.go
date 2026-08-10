package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	_ "crypto/sha256" // registers SHA-256 for crypto.Hash.New() (used by HS256/RS256/ES256)
	_ "crypto/sha512" // registers SHA-384/512 for crypto.Hash.New()
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// This file is the zero-dependency JWT (JWS) validation core behind BearerAuth (bearer.go), the Go
// counterpart of Benzene.Auth.OAuth2's OAuth2BearerMiddleware. .NET leans on
// Microsoft.IdentityModel; Go's standard library (crypto/hmac, crypto/rsa, crypto/ecdsa,
// encoding/base64, encoding/json) covers everything a compact-serialization JWS validator needs, so
// this stays in the zero-dependency auth package. It deliberately supports only signature validation
// of the three standard algorithm families (HMAC, RSA-PKCS1v15, ECDSA); encryption (JWE) and the
// exotic algorithms are out of scope.
//
// Security posture, matching the .NET package and RFC 8725:
//   - The signing algorithm is checked against an explicit allowlist BEFORE any key is resolved or
//     signature verified - a validator that trusts whatever "alg" the token claims is open to
//     algorithm-confusion (RFC 8725 §3.1). "none" is never accepted.
//   - The verification key is strongly typed per family (HMAC secret vs RSA public key vs ECDSA
//     public key), so an RS* token can never be verified against an HMAC secret (the classic
//     public-key-as-HMAC-secret confusion) - the family's verifier only reads its own key field.
//   - Every failure reason is a distinct (testable) error, but BearerAuth collapses them all to one
//     generic "invalid bearer token" so the caller learns nothing that would help forge a token.

// Algorithm is a JWS "alg" header value this package can verify.
type Algorithm string

const (
	HS256 Algorithm = "HS256"
	HS384 Algorithm = "HS384"
	HS512 Algorithm = "HS512"
	RS256 Algorithm = "RS256"
	RS384 Algorithm = "RS384"
	RS512 Algorithm = "RS512"
	ES256 Algorithm = "ES256"
	ES384 Algorithm = "ES384"
	ES512 Algorithm = "ES512"
)

// Validation errors. Each failure mode is a distinct error so tests (and an optional OnError hook)
// can tell them apart; BearerAuth never surfaces which one occurred to the caller.
var (
	ErrMalformedToken            = errors.New("jwt: malformed token")
	ErrAlgorithmNotAllowed       = errors.New("jwt: signing algorithm not allowed")
	ErrUnknownAlgorithm          = errors.New("jwt: unknown or unsupported signing algorithm")
	ErrCriticalHeaderUnsupported = errors.New("jwt: token uses an unsupported critical header extension")
	ErrNoKey                     = errors.New("jwt: no verification key for token")
	ErrSignatureInvalid          = errors.New("jwt: signature is invalid")
	ErrMissingExpiration         = errors.New("jwt: token has no expiration and one is required")
	ErrTokenExpired              = errors.New("jwt: token is expired")
	ErrTokenNotYetValid          = errors.New("jwt: token is not valid yet")
	ErrIssuedInFuture            = errors.New("jwt: token was issued in the future")
	ErrIssuerNotAllowed          = errors.New("jwt: issuer is not allowed")
	ErrAudienceNotAllowed        = errors.New("jwt: audience is not allowed")
)

// header is the decoded JWS protected header (only the fields this package uses).
type header struct {
	Alg  Algorithm `json:"alg"`
	Typ  string    `json:"typ"`
	Kid  string    `json:"kid"`
	Crit []string  `json:"crit"`
}

// Claims is a decoded JWT payload. Values keep their JSON types (string, float64, bool, []any,
// map[string]any), matching encoding/json's default decoding.
type Claims map[string]any

// String returns the named claim as a string when it is a JSON string, else ("", false).
func (c Claims) String(name string) (string, bool) {
	v, ok := c[name].(string)
	return v, ok
}

// audiences returns the "aud" claim normalized to a slice: JWT allows aud to be a single string or
// an array of strings (RFC 7519 §4.1.3). A missing/other-typed aud yields nil.
func (c Claims) audiences() []string {
	switch v := c["aud"].(type) {
	case string:
		return []string{v}
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

// numericDate reads an RFC 7519 NumericDate claim (seconds since the epoch, JSON number). ok is
// false when the claim is absent or not a number.
func (c Claims) numericDate(name string) (t time.Time, ok bool) {
	v, present := c[name]
	if !present {
		return time.Time{}, false
	}
	f, isNum := v.(float64)
	if !isNum {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// KeyResolver returns the candidate verification keys for a token, given its decoded header (so it
// can match on "kid"). Returning more than one key means "try each" - used when a token carries no
// kid and the resolver holds several. An error resolving keys is a validation failure.
type KeyResolver interface {
	ResolveKeys(ctx context.Context, kid string) ([]VerificationKey, error)
}

// Validator validates a compact-serialization JWT against a fixed security policy. Build it with
// NewValidator; it is safe for concurrent use (its KeyResolver must be too - the shipped ones are).
type Validator struct {
	allowedAlgs map[Algorithm]bool
	issuers     map[string]bool
	audiences   map[string]bool
	keys        KeyResolver
	clockSkew   time.Duration
	requireExp  bool
	now         func() time.Time
}

// ValidatorOption configures a Validator.
type ValidatorOption func(*Validator)

// WithRequireExpiration controls whether a token MUST carry a numeric "exp" claim. Default true,
// matching Microsoft.IdentityModel's RequireExpirationTime default: without it, a token with no (or a
// non-numeric) exp would have no lifetime bound and be valid forever - a leaked token that never
// expires. Set false only for a token profile that genuinely has no expiry.
func WithRequireExpiration(require bool) ValidatorOption {
	return func(v *Validator) { v.requireExp = require }
}

// WithClockSkew sets the tolerance applied to exp/nbf/iat checks (default 2 minutes, matching the
// .NET OAuth2BearerOptions default). Clock skew between the issuer and this service is normal; a
// small leeway avoids spurious rejections at the very edge of a token's lifetime.
func WithClockSkew(d time.Duration) ValidatorOption {
	return func(v *Validator) { v.clockSkew = d }
}

// WithClock overrides the time source (default time.Now) - for tests that mint tokens with fixed
// exp/nbf and assert the boundary behavior without real time passing.
func WithClock(now func() time.Time) ValidatorOption {
	return func(v *Validator) { v.now = now }
}

// NewValidator builds a Validator. Like the .NET OAuth2BearerOptions, every trust anchor is required
// with no permissive default, so a wiring mistake fails fast at construction rather than silently
// under-validating every token:
//
//   - allowedAlgs: the signing-algorithm allowlist (RFC 8725 §3.1). Must be non-empty.
//   - issuers: the trusted "iss" values. Must be non-empty - an empty list would accept any issuer.
//   - audiences: the accepted "aud" values. Must be non-empty - an empty list would accept a token
//     minted for any audience (the token-confusion mistake).
//   - keys: how verification keys are resolved (StaticKeys or a JWKS resolver).
//
// It panics on an empty allowlist/issuers/audiences or a nil resolver - all wiring-time programming
// errors, never a per-request condition.
func NewValidator(allowedAlgs []Algorithm, issuers, audiences []string, keys KeyResolver, opts ...ValidatorOption) *Validator {
	if len(allowedAlgs) == 0 {
		panic("auth: NewValidator requires at least one allowed algorithm (an empty allowlist would trust the token's own alg - RFC 8725 §3.1)")
	}
	if len(issuers) == 0 {
		panic("auth: NewValidator requires at least one trusted issuer (an empty list would accept any issuer)")
	}
	if len(audiences) == 0 {
		panic("auth: NewValidator requires at least one accepted audience (an empty list would accept a token minted for any audience)")
	}
	if keys == nil {
		panic("auth: NewValidator requires a non-nil KeyResolver")
	}
	for _, a := range allowedAlgs {
		if _, _, ok := algProperties(a); !ok {
			panic(fmt.Sprintf("auth: NewValidator got an unsupported algorithm %q (supported: HS/RS/ES 256/384/512)", a))
		}
	}
	v := &Validator{
		allowedAlgs: make(map[Algorithm]bool, len(allowedAlgs)),
		issuers:     make(map[string]bool, len(issuers)),
		audiences:   make(map[string]bool, len(audiences)),
		keys:        keys,
		clockSkew:   2 * time.Minute,
		requireExp:  true,
		now:         time.Now,
	}
	for _, a := range allowedAlgs {
		v.allowedAlgs[a] = true
	}
	for _, iss := range issuers {
		v.issuers[iss] = true
	}
	for _, aud := range audiences {
		v.audiences[aud] = true
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// Validate parses and fully validates token, returning its Claims on success. The order is
// deliberate: structural parse, then the algorithm allowlist (before any key work), then signature,
// then the registered claims (iss/aud/exp/nbf/iat). Any failure returns a wrapped sentinel error
// (see the Err* vars) and nil Claims.
func (v *Validator) Validate(ctx context.Context, token string) (Claims, error) {
	hdr, claims, signingInput, sig, err := parseToken(token)
	if err != nil {
		return nil, err
	}

	if len(hdr.Crit) > 0 {
		// RFC 7515 §4.1.11 / RFC 8725: a "crit" header names extensions the recipient MUST
		// understand. This validator understands none, so any crit header means reject.
		return nil, ErrCriticalHeaderUnsupported
	}

	if !v.allowedAlgs[hdr.Alg] {
		// Reject a disallowed alg (including "none") before touching keys, per RFC 8725 §3.1.
		return nil, fmt.Errorf("%w: %q", ErrAlgorithmNotAllowed, hdr.Alg)
	}

	keys, err := v.keys.ResolveKeys(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrNoKey
	}
	if err := verifyAny(hdr.Alg, signingInput, sig, keys); err != nil {
		return nil, err
	}

	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// validateClaims checks the registered claims against the policy, with clock skew applied to the
// time-based ones.
func (v *Validator) validateClaims(claims Claims) error {
	now := v.now()

	exp, hasExp := claims.numericDate("exp")
	if v.requireExp && !hasExp {
		// A missing (or non-numeric - numericDate reports that as absent) exp when one is required:
		// the token would otherwise never expire.
		return ErrMissingExpiration
	}
	if hasExp && now.After(exp.Add(v.clockSkew)) {
		return ErrTokenExpired
	}
	if nbf, ok := claims.numericDate("nbf"); ok && now.Before(nbf.Add(-v.clockSkew)) {
		return ErrTokenNotYetValid
	}
	if iat, ok := claims.numericDate("iat"); ok && now.Before(iat.Add(-v.clockSkew)) {
		return ErrIssuedInFuture
	}

	iss, _ := claims.String("iss")
	if !v.issuers[iss] {
		return fmt.Errorf("%w: %q", ErrIssuerNotAllowed, iss)
	}

	auds := claims.audiences()
	matched := false
	for _, aud := range auds {
		if v.audiences[aud] {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("%w: %v", ErrAudienceNotAllowed, auds)
	}
	return nil
}

// parseToken splits a compact JWS (header.payload.signature), base64url-decodes each part, and
// unmarshals the header and payload. signingInput is the ASCII "header.payload" over which the
// signature is computed (the raw bytes, not re-encoded).
func parseToken(token string) (hdr header, claims Claims, signingInput, sig []byte, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return header{}, nil, nil, nil, ErrMalformedToken
	}
	headerBytes, err := decodeSegment(parts[0])
	if err != nil {
		return header{}, nil, nil, nil, fmt.Errorf("%w: header not base64url", ErrMalformedToken)
	}
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return header{}, nil, nil, nil, fmt.Errorf("%w: header not JSON", ErrMalformedToken)
	}
	payloadBytes, err := decodeSegment(parts[1])
	if err != nil {
		return header{}, nil, nil, nil, fmt.Errorf("%w: payload not base64url", ErrMalformedToken)
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return header{}, nil, nil, nil, fmt.Errorf("%w: payload not JSON", ErrMalformedToken)
	}
	sig, err = decodeSegment(parts[2])
	if err != nil {
		return header{}, nil, nil, nil, fmt.Errorf("%w: signature not base64url", ErrMalformedToken)
	}
	signingInput = []byte(parts[0] + "." + parts[1])
	return hdr, claims, signingInput, sig, nil
}

// decodeSegment base64url-decodes a JWS segment. JWS uses unpadded base64url (RFC 7515 §2), so this
// uses RawURLEncoding.
func decodeSegment(seg string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(seg)
}

// verifyAny returns nil if the signature verifies against ANY candidate key (used when a token has
// no kid and the resolver returns several). A key of the wrong family for alg (e.g. an RSA key for
// an HS* token) is skipped, not treated as a mismatch. If at least one right-family key was tried
// and none matched, it is ErrSignatureInvalid; if no candidate key was even of the right family, it
// is ErrNoKey.
func verifyAny(alg Algorithm, signingInput, sig []byte, keys []VerificationKey) error {
	anyRightFamily := false
	for _, key := range keys {
		ok, err := verify(alg, signingInput, sig, key)
		if errors.Is(err, errKeyWrongFamily) {
			continue
		}
		if err != nil {
			return err
		}
		anyRightFamily = true
		if ok {
			return nil
		}
	}
	if !anyRightFamily {
		return ErrNoKey
	}
	return ErrSignatureInvalid
}

// errKeyWrongFamily marks a key that is not of the algorithm's family, so verifyAny can tell it
// apart from a genuine signature mismatch. It never escapes this file.
var errKeyWrongFamily = errors.New("jwt: key is not of the algorithm's family")

// verify checks one key against the signature. It returns (true,nil) on a valid signature,
// (false,nil) on a valid-family key whose signature does not match, and (false, errKeyWrongFamily)
// when key is not of alg's family so the caller should try another key.
func verify(alg Algorithm, signingInput, sig []byte, key VerificationKey) (bool, error) {
	h, family, ok := algProperties(alg)
	if !ok {
		return false, ErrUnknownAlgorithm
	}
	switch family {
	case familyHMAC:
		if key.HMAC == nil {
			return false, errKeyWrongFamily
		}
		mac := hmac.New(h.New, key.HMAC)
		mac.Write(signingInput)
		return hmac.Equal(sig, mac.Sum(nil)), nil
	case familyRSA:
		if key.RSA == nil {
			return false, errKeyWrongFamily
		}
		digest := hashBytes(h, signingInput)
		return rsa.VerifyPKCS1v15(key.RSA, h, digest, sig) == nil, nil
	case familyECDSA:
		if key.ECDSA == nil {
			return false, errKeyWrongFamily
		}
		return verifyECDSA(h, key.ECDSA, signingInput, sig), nil
	default:
		// Unreachable: algProperties returned ok above, so family is one of the three cases.
		// Kept for switch exhaustiveness only.
		return false, ErrUnknownAlgorithm
	}
}

type algFamily int

const (
	familyHMAC algFamily = iota
	familyRSA
	familyECDSA
)

// algProperties maps an algorithm to its hash and family. ok is false for an unsupported alg.
func algProperties(alg Algorithm) (h crypto.Hash, family algFamily, ok bool) {
	switch alg {
	case HS256:
		return crypto.SHA256, familyHMAC, true
	case HS384:
		return crypto.SHA384, familyHMAC, true
	case HS512:
		return crypto.SHA512, familyHMAC, true
	case RS256:
		return crypto.SHA256, familyRSA, true
	case RS384:
		return crypto.SHA384, familyRSA, true
	case RS512:
		return crypto.SHA512, familyRSA, true
	case ES256:
		return crypto.SHA256, familyECDSA, true
	case ES384:
		return crypto.SHA384, familyECDSA, true
	case ES512:
		return crypto.SHA512, familyECDSA, true
	default:
		return 0, 0, false
	}
}

// hashBytes computes h over data. h is one of SHA-256/384/512, all linked in via the crypto/sha256
// and crypto/sha512 imports, so h.New() is always available here.
func hashBytes(h crypto.Hash, data []byte) []byte {
	hh := h.New()
	hh.Write(data)
	return hh.Sum(nil)
}

// verifyECDSA verifies a JWS ECDSA signature, which is the fixed-width concatenation r||s (RFC 7518
// §3.4), NOT ASN.1 - each of r and s is left-padded to the curve's byte size. It returns false for a
// wrong-length signature rather than erroring, since a malformed signature is simply an invalid one.
func verifyECDSA(h crypto.Hash, pub *ecdsa.PublicKey, signingInput, sig []byte) bool {
	keyBytes := (pub.Curve.Params().BitSize + 7) / 8
	if len(sig) != 2*keyBytes {
		return false
	}
	r := new(big.Int).SetBytes(sig[:keyBytes])
	s := new(big.Int).SetBytes(sig[keyBytes:])
	return ecdsa.Verify(pub, hashBytes(h, signingInput), r, s)
}
