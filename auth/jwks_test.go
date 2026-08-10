package auth

import (
	"context"
	"crypto/elliptic"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// jwksServer serves a JWKS document whose body is swappable, counting fetches. It runs over plain
// HTTP, so tests use WithRequireHTTPS(false).
type jwksServer struct {
	server *httptest.Server
	body   atomic.Value // string
	hits   atomic.Int32
	status atomic.Int32
}

func newJWKSServer(t *testing.T, initial string) *jwksServer {
	t.Helper()
	js := &jwksServer{}
	js.body.Store(initial)
	js.status.Store(http.StatusOK)
	js.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		js.hits.Add(1)
		if code := js.status.Load(); code != http.StatusOK {
			w.WriteHeader(int(code))
			return
		}
		_, _ = w.Write([]byte(js.body.Load().(string)))
	}))
	t.Cleanup(js.server.Close)
	return js
}

func jwksDoc(t *testing.T, keys ...map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"keys": keys})
	return string(b)
}

func TestJWKSResolver_FetchesAndCaches(t *testing.T) {
	rk, _ := keys(t)
	js := newJWKSServer(t, jwksDoc(t, rsaJWK(t, "r1", &rk.PublicKey)))
	r, err := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false))
	if err != nil {
		t.Fatalf("NewJWKSResolver: %v", err)
	}

	for i := 0; i < 3; i++ {
		got, err := r.ResolveKeys(context.Background(), "r1")
		if err != nil || len(got) != 1 || got[0].RSA == nil {
			t.Fatalf("ResolveKeys #%d = %v,%v", i, got, err)
		}
	}
	if js.hits.Load() != 1 {
		t.Errorf("JWKS fetched %d times, want 1 (cached after first)", js.hits.Load())
	}
}

func TestJWKSResolver_EndToEndValidation(t *testing.T) {
	rk, _ := keys(t)
	js := newJWKSServer(t, jwksDoc(t, rsaJWK(t, "r1", &rk.PublicKey)))
	r, _ := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false))
	v := NewValidator([]Algorithm{RS256}, []string{"https://issuer.example"}, []string{"my-service"}, r)

	if _, err := v.Validate(context.Background(), makeToken(t, RS256, "r1", goodClaims())); err != nil {
		t.Fatalf("Validate via JWKS: %v", err)
	}
}

func TestJWKSResolver_KidRotationRefetch(t *testing.T) {
	rk, ek := keys(t)
	js := newJWKSServer(t, jwksDoc(t, rsaJWK(t, "r1", &rk.PublicKey)))
	now := time.Unix(1_700_000_000, 0)
	r, _ := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false),
		WithMinRefetchInterval(time.Minute), withJWKSClock(func() time.Time { return now }))

	// Prime the cache (kid r1).
	if _, err := r.ResolveKeys(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	// Rotate the document to add e1; asking for the unknown kid within the throttle window does NOT
	// refetch.
	js.body.Store(jwksDoc(t, rsaJWK(t, "r1", &rk.PublicKey), ecJWK(t, "e1", &ek[elliptic.P256()].PublicKey, "P-256")))
	if got, _ := r.ResolveKeys(context.Background(), "e1"); len(got) != 0 {
		t.Errorf("unknown kid within throttle returned %d keys, want 0 (no refetch yet)", len(got))
	}
	if js.hits.Load() != 1 {
		t.Errorf("hits = %d, want 1 (throttled)", js.hits.Load())
	}
	// Advance past the throttle window: now the unknown kid triggers a refetch and resolves.
	now = now.Add(2 * time.Minute)
	got, err := r.ResolveKeys(context.Background(), "e1")
	if err != nil || len(got) != 1 || got[0].ECDSA == nil {
		t.Fatalf("after refetch: %v,%v", got, err)
	}
	if js.hits.Load() != 2 {
		t.Errorf("hits = %d, want 2 (one refetch)", js.hits.Load())
	}
}

func TestJWKSResolver_RefetchError(t *testing.T) {
	rk, _ := keys(t)
	js := newJWKSServer(t, jwksDoc(t, rsaJWK(t, "r1", &rk.PublicKey)))
	now := time.Unix(1_700_000_000, 0)
	r, _ := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false),
		WithMinRefetchInterval(time.Minute), withJWKSClock(func() time.Time { return now }))

	if _, err := r.ResolveKeys(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	// The endpoint starts failing; an unknown kid past the throttle window triggers a refetch that
	// now errors, and that error surfaces.
	js.status.Store(http.StatusInternalServerError)
	now = now.Add(2 * time.Minute)
	if _, err := r.ResolveKeys(context.Background(), "unknown"); err == nil {
		t.Error("want the refetch error surfaced")
	}
}

func TestJWKSResolver_NoKidReturnsKeyless(t *testing.T) {
	rk, _ := keys(t)
	js := newJWKSServer(t, jwksDoc(t, rsaJWK(t, "", &rk.PublicKey))) // keyless
	r, _ := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false))
	got, err := r.ResolveKeys(context.Background(), "")
	if err != nil || len(got) != 1 {
		t.Fatalf("ResolveKeys(no kid) = %v,%v, want the keyless key", got, err)
	}
}

func TestJWKSResolver_FetchErrors(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		js := newJWKSServer(t, "")
		js.status.Store(http.StatusInternalServerError)
		r, _ := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false))
		if _, err := r.ResolveKeys(context.Background(), "x"); err == nil {
			t.Error("want an error on a non-200 JWKS response")
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		r, _ := NewJWKSResolver("http://127.0.0.1:1/jwks", WithRequireHTTPS(false))
		if _, err := r.ResolveKeys(context.Background(), "x"); err == nil {
			t.Error("want an error when the JWKS endpoint is unreachable")
		}
	})
	t.Run("invalid jwks body", func(t *testing.T) {
		js := newJWKSServer(t, "not json")
		r, _ := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false))
		if _, err := r.ResolveKeys(context.Background(), "x"); err == nil {
			t.Error("want an error for a non-JSON JWKS body")
		}
	})
}

func TestJWKSResolver_RequireHTTPS(t *testing.T) {
	if _, err := NewJWKSResolver("http://insecure.example/jwks"); err == nil {
		t.Error("want an error for a plain-http JWKS URL with https required (the default)")
	}
	if _, err := NewJWKSResolver("://bad"); err == nil {
		t.Error("want an error for an unparseable URL")
	}
	if _, err := NewJWKSResolver("https://ok.example/jwks"); err != nil {
		t.Errorf("https URL should be accepted, got %v", err)
	}
}

func TestNewJWKSFromAuthority(t *testing.T) {
	rk, _ := keys(t)

	t.Run("discovers jwks_uri then resolves", func(t *testing.T) {
		jwks := newJWKSServer(t, jwksDoc(t, rsaJWK(t, "r1", &rk.PublicKey)))
		disco := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jwks_uri": jwks.server.URL})
		}))
		t.Cleanup(disco.Close)

		r, err := NewJWKSFromAuthority(context.Background(), disco.URL, WithRequireHTTPS(false))
		if err != nil {
			t.Fatalf("NewJWKSFromAuthority: %v", err)
		}
		got, err := r.ResolveKeys(context.Background(), "r1")
		if err != nil || len(got) != 1 {
			t.Fatalf("ResolveKeys after discovery = %v,%v", got, err)
		}
	})

	t.Run("discovery https required", func(t *testing.T) {
		if _, err := NewJWKSFromAuthority(context.Background(), "http://insecure.example"); err == nil {
			t.Error("want an error for a plain-http authority with https required")
		}
	})

	t.Run("discovery document without jwks_uri", func(t *testing.T) {
		disco := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(disco.Close)
		if _, err := NewJWKSFromAuthority(context.Background(), disco.URL, WithRequireHTTPS(false)); err == nil {
			t.Error("want an error when the discovery document has no jwks_uri")
		}
	})

	t.Run("discovery document not json", func(t *testing.T) {
		disco := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}))
		t.Cleanup(disco.Close)
		if _, err := NewJWKSFromAuthority(context.Background(), disco.URL, WithRequireHTTPS(false)); err == nil {
			t.Error("want an error for a non-JSON discovery document")
		}
	})

	t.Run("discovery endpoint unreachable", func(t *testing.T) {
		if _, err := NewJWKSFromAuthority(context.Background(), "http://127.0.0.1:1", WithRequireHTTPS(false)); err == nil {
			t.Error("want an error when the discovery endpoint is unreachable")
		}
	})
}

// erroringReadCloser fails on Read, to exercise get's body-read error path.
type erroringReadCloser struct{}

func (erroringReadCloser) Read([]byte) (int, error) { return 0, errors.New("read boom") }
func (erroringReadCloser) Close() error             { return nil }

type erroringBodyTransport struct{}

func (erroringBodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: erroringReadCloser{}, Header: make(http.Header)}, nil
}

func TestJWKSResolver_BodyReadError(t *testing.T) {
	r, _ := NewJWKSResolver("https://ok.example/jwks", WithHTTPClient(&http.Client{Transport: erroringBodyTransport{}}))
	if _, err := r.ResolveKeys(context.Background(), "x"); err == nil {
		t.Error("want an error when the JWKS body fails to read")
	}
}

func TestJWKSResolver_CustomHTTPClient(t *testing.T) {
	rk, _ := keys(t)
	js := newJWKSServer(t, jwksDoc(t, rsaJWK(t, "r1", &rk.PublicKey)))
	r, _ := NewJWKSResolver(js.server.URL, WithRequireHTTPS(false), WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if _, err := r.ResolveKeys(context.Background(), "r1"); err != nil {
		t.Fatalf("ResolveKeys with a custom client: %v", err)
	}
}
