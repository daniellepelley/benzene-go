package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// RSA modulus / exponent bounds applied when parsing a JWK, to reject weak keys and keep signature
// verification from becoming an attacker-influenced CPU sink.
const (
	minRSABits     = 2048      // reject anything weaker than a modern minimum
	maxRSABits     = 16384     // cap verification cost
	maxRSAExponent = 1<<31 - 1 // keep E within a 32-bit int (rsa.PublicKey.E is an int)
)

// VerificationKey is one candidate key for verifying a JWS signature. Exactly one of the fields is
// set; the field that is set determines which algorithm family the key can verify (HMAC / RSA /
// ECDSA), which is what structurally prevents cross-family key confusion (jwt.go).
type VerificationKey struct {
	HMAC  []byte
	RSA   *rsa.PublicKey
	ECDSA *ecdsa.PublicKey
}

// StaticKeys is a fixed set of verification keys - the zero-dependency KeyResolver for services that
// hold their signing keys directly (a shared HMAC secret, or the identity provider's public keys
// pinned at deploy time) rather than fetching a JWKS. Keys may be registered under a "kid" (matched
// to the token header's kid) and/or without one (tried when the token carries no kid, or no kid
// matched).
type StaticKeys struct {
	byKid   map[string]VerificationKey
	keyless []VerificationKey
}

// NewStaticKeys builds an empty StaticKeys. Add keys with AddHMAC/AddRSA/AddECDSA (chainable).
func NewStaticKeys() *StaticKeys {
	return &StaticKeys{byKid: map[string]VerificationKey{}}
}

// AddHMAC registers an HMAC secret (for HS256/384/512). Pass kid "" to register it keyless.
func (s *StaticKeys) AddHMAC(kid string, secret []byte) *StaticKeys {
	return s.add(kid, VerificationKey{HMAC: secret})
}

// AddRSA registers an RSA public key (for RS256/384/512). Pass kid "" to register it keyless.
func (s *StaticKeys) AddRSA(kid string, key *rsa.PublicKey) *StaticKeys {
	return s.add(kid, VerificationKey{RSA: key})
}

// AddECDSA registers an ECDSA public key (for ES256/384/512). Pass kid "" to register it keyless.
func (s *StaticKeys) AddECDSA(kid string, key *ecdsa.PublicKey) *StaticKeys {
	return s.add(kid, VerificationKey{ECDSA: key})
}

func (s *StaticKeys) add(kid string, key VerificationKey) *StaticKeys {
	if kid == "" {
		s.keyless = append(s.keyless, key)
	} else {
		s.byKid[kid] = key
	}
	return s
}

// ResolveKeys returns the key registered under kid (if any) plus every keyless key, so a token with
// a matching kid resolves precisely while one without a kid falls back to trying the keyless set.
func (s *StaticKeys) ResolveKeys(_ context.Context, kid string) ([]VerificationKey, error) {
	var keys []VerificationKey
	if kid != "" {
		if k, ok := s.byKid[kid]; ok {
			keys = append(keys, k)
		}
	}
	keys = append(keys, s.keyless...)
	return keys, nil
}

// jwk is one key from a JWKS document (RFC 7517), limited to the fields this package reads.
type jwk struct {
	Kty string `json:"kty"` // "RSA" or "EC"
	Kid string `json:"kid"`
	Use string `json:"use"` // "sig" or "enc" (encryption keys are skipped)
	Alg string `json:"alg"`
	// RSA
	N string `json:"n"`
	E string `json:"e"`
	// EC
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// jwkSet is a JWKS document.
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// errUnsupportedJWK marks a JWK this package does not turn into a verification key (an unknown key
// type, an encryption key, or a malformed one) so parseJWKSet can skip it without failing the whole
// set. It never escapes this file.
var errUnsupportedJWK = errors.New("jwt: unsupported or malformed JWK")

// toVerificationKey converts a signature JWK into a VerificationKey. It returns errUnsupportedJWK for
// an encryption-use key or an unhandled key type (RSA and EC are supported), and a real error for a
// key that claims a supported type but is malformed (bad base64 / bad curve).
func (k jwk) toVerificationKey() (VerificationKey, error) {
	if k.Use == "enc" {
		return VerificationKey{}, errUnsupportedJWK
	}
	switch k.Kty {
	case "RSA":
		n, err := decodeBigInt(k.N)
		if err != nil {
			return VerificationKey{}, fmt.Errorf("jwt: bad RSA modulus in JWK: %w", err)
		}
		// Bound the modulus size: reject a weak key (< 2048 bits) and cap the maximum so an
		// oversized modulus can't turn each rsa.VerifyPKCS1v15 into an attacker-influenced CPU sink.
		if bits := n.BitLen(); bits < minRSABits || bits > maxRSABits {
			return VerificationKey{}, fmt.Errorf("jwt: RSA modulus of %d bits is outside the allowed [%d,%d]", bits, minRSABits, maxRSABits)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return VerificationKey{}, fmt.Errorf("jwt: bad RSA exponent in JWK: %w", err)
		}
		e := new(big.Int).SetBytes(eBytes)
		// The exponent must be a sane, small odd integer that fits a platform int on 32-bit too
		// (rsa.PublicKey.E is an int), so cap it well below math.MaxInt32.
		if !e.IsInt64() || e.Int64() < 3 || e.Int64() > maxRSAExponent {
			return VerificationKey{}, fmt.Errorf("jwt: implausible RSA exponent in JWK")
		}
		return VerificationKey{RSA: &rsa.PublicKey{N: n, E: int(e.Int64())}}, nil
	case "EC":
		curve, ok := curveFor(k.Crv)
		if !ok {
			return VerificationKey{}, fmt.Errorf("jwt: unsupported EC curve %q in JWK", k.Crv)
		}
		x, err := decodeBigInt(k.X)
		if err != nil {
			return VerificationKey{}, fmt.Errorf("jwt: bad EC x in JWK: %w", err)
		}
		y, err := decodeBigInt(k.Y)
		if err != nil {
			return VerificationKey{}, fmt.Errorf("jwt: bad EC y in JWK: %w", err)
		}
		if !curve.IsOnCurve(x, y) {
			return VerificationKey{}, fmt.Errorf("jwt: EC point in JWK is not on curve %q", k.Crv)
		}
		return VerificationKey{ECDSA: &ecdsa.PublicKey{Curve: curve, X: x, Y: y}}, nil
	default:
		return VerificationKey{}, errUnsupportedJWK
	}
}

// curveFor maps a JWK "crv" name to its elliptic curve (RFC 7518 §6.2.1.1).
func curveFor(crv string) (elliptic.Curve, bool) {
	switch crv {
	case "P-256":
		return elliptic.P256(), true
	case "P-384":
		return elliptic.P384(), true
	case "P-521":
		return elliptic.P521(), true
	default:
		return nil, false
	}
}

// decodeBigInt base64url-decodes a JWK big-endian integer field.
func decodeBigInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("empty")
	}
	return new(big.Int).SetBytes(b), nil
}

// parseJWKSet parses a JWKS document into keys indexed by kid plus a keyless list (keys with no
// kid). A single unsupported key (encryption-use or unknown type) is skipped; a key that claims a
// supported type but is malformed fails the parse, so a corrupt document is not silently treated as
// "no keys".
func parseJWKSet(body []byte) (byKid map[string][]VerificationKey, keyless []VerificationKey, err error) {
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, nil, fmt.Errorf("jwt: JWKS is not valid JSON: %w", err)
	}
	byKid = map[string][]VerificationKey{}
	for _, k := range set.Keys {
		vk, err := k.toVerificationKey()
		if errors.Is(err, errUnsupportedJWK) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if k.Kid == "" {
			keyless = append(keyless, vk)
		} else {
			byKid[k.Kid] = append(byKid[k.Kid], vk)
		}
	}
	return byKid, keyless, nil
}
