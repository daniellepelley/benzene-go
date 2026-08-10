package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- test signing helpers (no third-party JWT library) ----

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signingKeys are generated once and reused across tests (RSA/ECDSA keygen is comparatively slow).
var (
	keyOnce sync.Once
	rsaKey  *rsa.PrivateKey
	ecKeys  map[elliptic.Curve]*ecdsa.PrivateKey
	hmacKey = []byte("a-shared-secret-of-reasonable-length-0123456789")
)

func keys(t *testing.T) (*rsa.PrivateKey, map[elliptic.Curve]*ecdsa.PrivateKey) {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		rsaKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		ecKeys = map[elliptic.Curve]*ecdsa.PrivateKey{}
		for _, c := range []elliptic.Curve{elliptic.P256(), elliptic.P384(), elliptic.P521()} {
			k, err := ecdsa.GenerateKey(c, rand.Reader)
			if err != nil {
				panic(err)
			}
			ecKeys[c] = k
		}
	})
	return rsaKey, ecKeys
}

// signKeyFor returns the private signing key for alg (the *rsa.PrivateKey / *ecdsa.PrivateKey /
// HMAC secret) matching the public/secret this test file registers with StaticKeys.
func signKeyFor(t *testing.T, alg Algorithm) any {
	t.Helper()
	rk, ek := keys(t)
	switch alg {
	case HS256, HS384, HS512:
		return hmacKey
	case RS256, RS384, RS512:
		return rk
	case ES256:
		return ek[elliptic.P256()]
	case ES384:
		return ek[elliptic.P384()]
	case ES512:
		return ek[elliptic.P521()]
	}
	panic("unknown alg")
}

// verificationKeyFor returns the StaticKeys entry corresponding to signKeyFor(alg).
func verificationKeyFor(t *testing.T, alg Algorithm) VerificationKey {
	t.Helper()
	rk, ek := keys(t)
	switch alg {
	case HS256, HS384, HS512:
		return VerificationKey{HMAC: hmacKey}
	case RS256, RS384, RS512:
		return VerificationKey{RSA: &rk.PublicKey}
	case ES256:
		return VerificationKey{ECDSA: &ek[elliptic.P256()].PublicKey}
	case ES384:
		return VerificationKey{ECDSA: &ek[elliptic.P384()].PublicKey}
	case ES512:
		return VerificationKey{ECDSA: &ek[elliptic.P521()].PublicKey}
	}
	panic("unknown alg")
}

func sign(t *testing.T, alg Algorithm, key any, input []byte) []byte {
	t.Helper()
	h, family, _ := algProperties(alg)
	switch family {
	case familyHMAC:
		mac := hmac.New(h.New, key.([]byte))
		mac.Write(input)
		return mac.Sum(nil)
	case familyRSA:
		sig, err := rsa.SignPKCS1v15(rand.Reader, key.(*rsa.PrivateKey), h, hashBytes(h, input))
		if err != nil {
			t.Fatalf("rsa sign: %v", err)
		}
		return sig
	default: // familyECDSA
		priv := key.(*ecdsa.PrivateKey)
		r, s, err := ecdsa.Sign(rand.Reader, priv, hashBytes(h, input))
		if err != nil {
			t.Fatalf("ecdsa sign: %v", err)
		}
		keyBytes := (priv.Curve.Params().BitSize + 7) / 8
		out := make([]byte, 2*keyBytes)
		r.FillBytes(out[:keyBytes])
		s.FillBytes(out[keyBytes:])
		return out
	}
}

// makeToken builds a signed compact JWT with the given header alg/kid and claims.
func makeToken(t *testing.T, alg Algorithm, kid string, claims map[string]any) string {
	t.Helper()
	hdr := map[string]any{"alg": string(alg), "typ": "JWT"}
	if kid != "" {
		hdr["kid"] = kid
	}
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	signingInput := b64(hb) + "." + b64(pb)
	sig := sign(t, alg, signKeyFor(t, alg), []byte(signingInput))
	return signingInput + "." + b64(sig)
}

// goodClaims are a valid iss/aud/exp set the fixed test validator accepts.
func goodClaims() map[string]any {
	return map[string]any{
		"iss": "https://issuer.example",
		"aud": "my-service",
		"sub": "user-123",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
}

// fixedValidator builds a validator trusting the test issuer/audience for alg, keyed by
// verificationKeyFor(alg) under an optional kid.
func fixedValidator(t *testing.T, alg Algorithm, kid string, opts ...ValidatorOption) *Validator {
	t.Helper()
	sk := NewStaticKeys().add(kid, verificationKeyFor(t, alg))
	return NewValidator([]Algorithm{alg}, []string{"https://issuer.example"}, []string{"my-service"}, sk, opts...)
}

// ---- tests ----

func TestValidator_ValidTokenAllAlgorithms(t *testing.T) {
	for _, alg := range []Algorithm{HS256, HS384, HS512, RS256, RS384, RS512, ES256, ES384, ES512} {
		t.Run(string(alg), func(t *testing.T) {
			v := fixedValidator(t, alg, "")
			token := makeToken(t, alg, "", goodClaims())
			claims, err := v.Validate(context.Background(), token)
			if err != nil {
				t.Fatalf("Validate(%s) = %v, want nil", alg, err)
			}
			if sub, _ := claims.String("sub"); sub != "user-123" {
				t.Errorf("sub = %q, want user-123", sub)
			}
		})
	}
}

func TestValidator_TamperedSignatureRejected(t *testing.T) {
	v := fixedValidator(t, RS256, "")
	token := makeToken(t, RS256, "", goodClaims())
	// Flip the last character of the signature segment.
	tampered := token[:len(token)-1]
	if token[len(token)-1] == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}
	if _, err := v.Validate(context.Background(), tampered); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("err = %v, want ErrSignatureInvalid", err)
	}
}

func TestValidator_AlgorithmNotAllowed(t *testing.T) {
	// Validator allows only HS256; the token is signed RS256, so it is rejected on the allowlist
	// before any key is even resolved.
	v := NewValidator([]Algorithm{HS256}, []string{"https://issuer.example"}, []string{"my-service"},
		NewStaticKeys().AddHMAC("", hmacKey))
	token := makeToken(t, RS256, "", goodClaims())
	if _, err := v.Validate(context.Background(), token); !errors.Is(err, ErrAlgorithmNotAllowed) {
		t.Fatalf("err = %v, want ErrAlgorithmNotAllowed", err)
	}
}

func TestValidator_NoneAlgorithmRejected(t *testing.T) {
	v := fixedValidator(t, HS256, "")
	// Hand-craft an unsigned "alg":"none" token.
	hb, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	pb, _ := json.Marshal(goodClaims())
	token := b64(hb) + "." + b64(pb) + "."
	if _, err := v.Validate(context.Background(), token); !errors.Is(err, ErrAlgorithmNotAllowed) {
		t.Fatalf("err = %v, want ErrAlgorithmNotAllowed for none", err)
	}
}

func TestValidator_TimeClaims(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := WithClock(func() time.Time { return now })

	tests := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr error
	}{
		{"expired", func(c map[string]any) { c["exp"] = float64(now.Add(-time.Hour).Unix()) }, ErrTokenExpired},
		{"expired within skew ok", func(c map[string]any) { c["exp"] = float64(now.Add(-time.Minute).Unix()) }, nil},
		{"not yet valid", func(c map[string]any) { c["nbf"] = float64(now.Add(time.Hour).Unix()) }, ErrTokenNotYetValid},
		{"nbf within skew ok", func(c map[string]any) { c["nbf"] = float64(now.Add(time.Minute).Unix()) }, nil},
		{"issued in future", func(c map[string]any) { c["iat"] = float64(now.Add(time.Hour).Unix()) }, ErrIssuedInFuture},
		{"non-numeric exp ignored", func(c map[string]any) { c["exp"] = "not-a-number" }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := fixedValidator(t, HS256, "", clock)
			claims := goodClaims()
			claims["exp"] = float64(now.Add(time.Hour).Unix()) // valid baseline
			tt.mutate(claims)
			token := makeToken(t, HS256, "", claims)
			_, err := v.Validate(context.Background(), token)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_IssuerAndAudience(t *testing.T) {
	t.Run("wrong issuer", func(t *testing.T) {
		v := fixedValidator(t, HS256, "")
		c := goodClaims()
		c["iss"] = "https://evil.example"
		if _, err := v.Validate(context.Background(), makeToken(t, HS256, "", c)); !errors.Is(err, ErrIssuerNotAllowed) {
			t.Fatalf("err = %v, want ErrIssuerNotAllowed", err)
		}
	})
	t.Run("wrong audience", func(t *testing.T) {
		v := fixedValidator(t, HS256, "")
		c := goodClaims()
		c["aud"] = "other-service"
		if _, err := v.Validate(context.Background(), makeToken(t, HS256, "", c)); !errors.Is(err, ErrAudienceNotAllowed) {
			t.Fatalf("err = %v, want ErrAudienceNotAllowed", err)
		}
	})
	t.Run("missing audience", func(t *testing.T) {
		v := fixedValidator(t, HS256, "")
		c := goodClaims()
		delete(c, "aud")
		if _, err := v.Validate(context.Background(), makeToken(t, HS256, "", c)); !errors.Is(err, ErrAudienceNotAllowed) {
			t.Fatalf("err = %v, want ErrAudienceNotAllowed for missing aud", err)
		}
	})
	t.Run("audience array matches", func(t *testing.T) {
		v := fixedValidator(t, HS256, "")
		c := goodClaims()
		c["aud"] = []any{"other-service", "my-service"}
		if _, err := v.Validate(context.Background(), makeToken(t, HS256, "", c)); err != nil {
			t.Fatalf("err = %v, want nil for matching aud in array", err)
		}
	})
}

func TestValidator_Malformed(t *testing.T) {
	v := fixedValidator(t, HS256, "")
	good := makeToken(t, HS256, "", goodClaims())
	parts := strings.Split(good, ".")

	cases := map[string]string{
		"two parts":          "a.b",
		"bad base64 header":  "!!!." + parts[1] + "." + parts[2],
		"bad base64 payload": parts[0] + ".!!!." + parts[2],
		"bad base64 sig":     parts[0] + "." + parts[1] + ".!!!",
		"header not json":    b64([]byte("not-json")) + "." + parts[1] + "." + parts[2],
		"payload not json":   parts[0] + "." + b64([]byte("not-json")) + "." + parts[2],
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Validate(context.Background(), token); !errors.Is(err, ErrMalformedToken) {
				t.Fatalf("err = %v, want ErrMalformedToken", err)
			}
		})
	}
}

func TestValidator_NoKeyForKid(t *testing.T) {
	// Validator keyed under "key-1"; token presents kid "key-2" and there is no keyless fallback.
	v := fixedValidator(t, HS256, "key-1")
	token := makeToken(t, HS256, "key-2", goodClaims())
	if _, err := v.Validate(context.Background(), token); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

func TestValidator_WrongFamilyKeyIsNoKey(t *testing.T) {
	// An HS256 token, but the only registered key is an RSA public key: no right-family key exists.
	rk, _ := keys(t)
	sk := NewStaticKeys().AddRSA("", &rk.PublicKey)
	v := NewValidator([]Algorithm{HS256}, []string{"https://issuer.example"}, []string{"my-service"}, sk)
	token := makeToken(t, HS256, "", goodClaims())
	if _, err := v.Validate(context.Background(), token); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

func TestValidator_MultipleKeylessKeysTriesEach(t *testing.T) {
	// Two keyless HMAC keys: the first is wrong, the second is correct - verifyAny must try both.
	sk := NewStaticKeys().AddHMAC("", []byte("wrong-secret")).AddHMAC("", hmacKey)
	v := NewValidator([]Algorithm{HS256}, []string{"https://issuer.example"}, []string{"my-service"}, sk)
	if _, err := v.Validate(context.Background(), makeToken(t, HS256, "", goodClaims())); err != nil {
		t.Fatalf("err = %v, want nil (second keyless key matches)", err)
	}
}

func TestNewValidator_Panics(t *testing.T) {
	sk := NewStaticKeys().AddHMAC("", hmacKey)
	cases := map[string]func(){
		"empty algs":      func() { NewValidator(nil, []string{"i"}, []string{"a"}, sk) },
		"empty issuers":   func() { NewValidator([]Algorithm{HS256}, nil, []string{"a"}, sk) },
		"empty audiences": func() { NewValidator([]Algorithm{HS256}, []string{"i"}, nil, sk) },
		"nil resolver":    func() { NewValidator([]Algorithm{HS256}, []string{"i"}, []string{"a"}, nil) },
		"unknown alg":     func() { NewValidator([]Algorithm{"FOO"}, []string{"i"}, []string{"a"}, sk) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			fn()
		})
	}
}

// White-box: verify defends against an unknown algorithm even though NewValidator/Validate prevent
// one reaching it through the public path.
func TestVerify_UnknownAlgorithmDefensive(t *testing.T) {
	if _, err := verify(Algorithm("FOO"), []byte("x"), []byte("y"), VerificationKey{HMAC: hmacKey}); !errors.Is(err, ErrUnknownAlgorithm) {
		t.Fatalf("err = %v, want ErrUnknownAlgorithm", err)
	}
	if err := verifyAny(Algorithm("FOO"), []byte("x"), []byte("y"), []VerificationKey{{HMAC: hmacKey}}); !errors.Is(err, ErrUnknownAlgorithm) {
		t.Fatalf("verifyAny err = %v, want ErrUnknownAlgorithm propagated", err)
	}
}

func TestValidator_ClockSkewToleratesExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// exp 30 minutes ago, but a 1h skew tolerance keeps it valid.
	v := fixedValidator(t, HS256, "", WithClock(func() time.Time { return now }), WithClockSkew(time.Hour))
	c := goodClaims()
	c["exp"] = float64(now.Add(-30 * time.Minute).Unix())
	if _, err := v.Validate(context.Background(), makeToken(t, HS256, "", c)); err != nil {
		t.Fatalf("err = %v, want nil (within the 1h clock skew)", err)
	}
}

// erroringResolver is a KeyResolver that always fails, to cover Validate's resolver-error path.
type erroringResolver struct{}

func (erroringResolver) ResolveKeys(context.Context, string) ([]VerificationKey, error) {
	return nil, errors.New("resolver down")
}

func TestValidator_KeyResolverError(t *testing.T) {
	v := NewValidator([]Algorithm{HS256}, []string{"https://issuer.example"}, []string{"my-service"}, erroringResolver{})
	if _, err := v.Validate(context.Background(), makeToken(t, HS256, "", goodClaims())); err == nil || err.Error() != "resolver down" {
		t.Fatalf("err = %v, want the resolver error surfaced", err)
	}
}

// A right-alg token with only a wrong-family key registered resolves to ErrNoKey - covering the
// RSA-family and ECDSA-family "key of the wrong type" branches of verify.
func TestValidator_WrongFamilyForRSAAndECDSA(t *testing.T) {
	for _, alg := range []Algorithm{RS256, ES256} {
		t.Run(string(alg), func(t *testing.T) {
			sk := NewStaticKeys().AddHMAC("", hmacKey) // only an HMAC key
			v := NewValidator([]Algorithm{alg}, []string{"https://issuer.example"}, []string{"my-service"}, sk)
			if _, err := v.Validate(context.Background(), makeToken(t, alg, "", goodClaims())); !errors.Is(err, ErrNoKey) {
				t.Fatalf("err = %v, want ErrNoKey (no %s-family key)", err, alg)
			}
		})
	}
}

func TestVerifyECDSA_WrongLengthSignature(t *testing.T) {
	_, ek := keys(t)
	pub := &ek[elliptic.P256()].PublicKey
	// A P-256 signature must be 64 bytes; a short one is simply invalid, not a panic.
	if verifyECDSA(crypto.SHA256, pub, []byte("input"), []byte{1, 2, 3}) {
		t.Error("verifyECDSA accepted a wrong-length signature")
	}
}
