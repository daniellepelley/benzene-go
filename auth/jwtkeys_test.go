package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
)

// rsaJWK builds a JWK JSON object for an RSA public key.
func rsaJWK(t *testing.T, kid string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	jwk := map[string]any{
		"kty": "RSA",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(eBytes),
	}
	if kid != "" {
		jwk["kid"] = kid
	}
	return jwk
}

// ecJWK builds a JWK JSON object for an ECDSA public key.
func ecJWK(t *testing.T, kid string, pub *ecdsa.PublicKey, crv string) map[string]any {
	t.Helper()
	size := (pub.Curve.Params().BitSize + 7) / 8
	x := make([]byte, size)
	y := make([]byte, size)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	jwk := map[string]any{
		"kty": "EC",
		"crv": crv,
		"x":   base64.RawURLEncoding.EncodeToString(x),
		"y":   base64.RawURLEncoding.EncodeToString(y),
	}
	if kid != "" {
		jwk["kid"] = kid
	}
	return jwk
}

func TestJWK_RSARoundTrip(t *testing.T) {
	rk, _ := keys(t)
	jwkJSON, _ := json.Marshal(rsaJWK(t, "r1", &rk.PublicKey))
	var k jwk
	_ = json.Unmarshal(jwkJSON, &k)

	vk, err := k.toVerificationKey()
	if err != nil {
		t.Fatalf("toVerificationKey: %v", err)
	}
	if vk.RSA == nil || vk.RSA.N.Cmp(rk.PublicKey.N) != 0 || vk.RSA.E != rk.PublicKey.E {
		t.Fatalf("parsed RSA key does not match the original")
	}
}

func TestJWK_ECRoundTrip(t *testing.T) {
	_, ek := keys(t)
	for crv, curve := range map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384(), "P-521": elliptic.P521()} {
		t.Run(crv, func(t *testing.T) {
			pub := &ek[curve].PublicKey
			jwkJSON, _ := json.Marshal(ecJWK(t, "e1", pub, crv))
			var k jwk
			_ = json.Unmarshal(jwkJSON, &k)

			vk, err := k.toVerificationKey()
			if err != nil {
				t.Fatalf("toVerificationKey: %v", err)
			}
			if vk.ECDSA == nil || vk.ECDSA.X.Cmp(pub.X) != 0 || vk.ECDSA.Y.Cmp(pub.Y) != 0 {
				t.Fatalf("parsed EC key does not match the original")
			}
		})
	}
}

func TestStaticKeys_AllFamiliesAndKid(t *testing.T) {
	rk, ek := keys(t)
	sk := NewStaticKeys().
		AddHMAC("h", hmacKey).
		AddRSA("r", &rk.PublicKey).
		AddECDSA("", &ek[elliptic.P256()].PublicKey) // keyless

	got, err := sk.ResolveKeys(context.Background(), "r")
	if err != nil {
		t.Fatal(err)
	}
	// The kid-matched RSA key plus the keyless ECDSA key.
	if len(got) != 2 || got[0].RSA == nil || got[1].ECDSA == nil {
		t.Errorf("ResolveKeys(r) = %+v, want [RSA(kid r), ECDSA(keyless)]", got)
	}
}

func TestJWK_SkippedAndMalformed(t *testing.T) {
	rk, ek := keys(t)
	tests := []struct {
		name     string
		jwk      jwk
		wantSkip bool // errUnsupportedJWK
		wantErr  bool // a real (malformed) error
	}{
		{name: "encryption use skipped", jwk: jwk{Kty: "RSA", Use: "enc"}, wantSkip: true},
		{name: "unknown kty skipped", jwk: jwk{Kty: "oct"}, wantSkip: true},
		{name: "bad rsa modulus", jwk: jwk{Kty: "RSA", N: "!!!", E: "AQAB"}, wantErr: true},
		{name: "rsa modulus too small", jwk: jwk{Kty: "RSA", N: base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3, 4}), E: "AQAB"}, wantErr: true},
		{name: "rsa modulus too large", jwk: jwk{Kty: "RSA", N: base64.RawURLEncoding.EncodeToString(new(big.Int).Lsh(big.NewInt(1), 20000).Bytes()), E: "AQAB"}, wantErr: true},
		{name: "bad rsa exponent", jwk: jwk{Kty: "RSA", N: base64.RawURLEncoding.EncodeToString(rk.N.Bytes()), E: "!!!"}, wantErr: true},
		{name: "implausible rsa exponent", jwk: jwk{Kty: "RSA", N: base64.RawURLEncoding.EncodeToString(rk.N.Bytes()), E: base64.RawURLEncoding.EncodeToString([]byte{1})}, wantErr: true},
		{name: "unknown ec curve", jwk: jwk{Kty: "EC", Crv: "P-999", X: "AA", Y: "AA"}, wantErr: true},
		{name: "bad ec x", jwk: jwk{Kty: "EC", Crv: "P-256", X: "!!!", Y: "AA"}, wantErr: true},
		{name: "bad ec y", jwk: jwk{Kty: "EC", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString(ek[elliptic.P256()].X.Bytes()), Y: "!!!"}, wantErr: true},
		{name: "empty ec x (decodeBigInt empty)", jwk: jwk{Kty: "EC", Crv: "P-256", X: "", Y: "AA"}, wantErr: true},
		{name: "ec point not on curve", jwk: jwk{Kty: "EC", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString([]byte{1}), Y: base64.RawURLEncoding.EncodeToString([]byte{2})}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.jwk.toVerificationKey()
			switch {
			case tt.wantSkip:
				if !errors.Is(err, errUnsupportedJWK) {
					t.Errorf("err = %v, want errUnsupportedJWK", err)
				}
			case tt.wantErr:
				if err == nil || errors.Is(err, errUnsupportedJWK) {
					t.Errorf("err = %v, want a malformed-key error", err)
				}
			}
		})
	}
}

func TestParseJWKSet(t *testing.T) {
	rk, ek := keys(t)

	t.Run("indexes by kid and keyless, skips enc", func(t *testing.T) {
		doc := map[string]any{"keys": []any{
			rsaJWK(t, "r1", &rk.PublicKey),
			ecJWK(t, "", &ek[elliptic.P256()].PublicKey, "P-256"), // keyless
			map[string]any{"kty": "RSA", "use": "enc"},            // skipped
		}}
		body, _ := json.Marshal(doc)
		byKid, keyless, err := parseJWKSet(body)
		if err != nil {
			t.Fatalf("parseJWKSet: %v", err)
		}
		if len(byKid["r1"]) != 1 || byKid["r1"][0].RSA == nil {
			t.Errorf("kid r1 not indexed as an RSA key: %+v", byKid)
		}
		if len(keyless) != 1 || keyless[0].ECDSA == nil {
			t.Errorf("keyless EC key not collected: %+v", keyless)
		}
	})

	t.Run("invalid JSON errors", func(t *testing.T) {
		if _, _, err := parseJWKSet([]byte("not json")); err == nil {
			t.Error("want an error for non-JSON JWKS")
		}
	})

	t.Run("a malformed key fails the whole set", func(t *testing.T) {
		doc := map[string]any{"keys": []any{jwk{Kty: "RSA", N: "!!!", E: "AQAB"}}}
		body, _ := json.Marshal(doc)
		if _, _, err := parseJWKSet(body); err == nil {
			t.Error("want an error when a key claims RSA but is malformed")
		}
	})
}

// A token verifies against a key resolved from a parsed JWK (the RS256 end-to-end path through
// StaticKeys built from a JWK).
func TestJWK_VerifiesToken(t *testing.T) {
	rk, _ := keys(t)
	jwkJSON, _ := json.Marshal(rsaJWK(t, "r1", &rk.PublicKey))
	var k jwk
	_ = json.Unmarshal(jwkJSON, &k)
	vk, _ := k.toVerificationKey()

	sk := &StaticKeys{byKid: map[string]VerificationKey{"r1": vk}}
	v := NewValidator([]Algorithm{RS256}, []string{"https://issuer.example"}, []string{"my-service"}, sk)
	if _, err := v.Validate(context.Background(), makeToken(t, RS256, "r1", goodClaims())); err != nil {
		t.Fatalf("Validate with a JWK-derived key: %v", err)
	}
}
