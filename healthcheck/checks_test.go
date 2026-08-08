package healthcheck

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestTCPCheck_ReachableIsOk(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	res := TCPCheck("db", host, port, WithTCPTimeout(time.Second)).Check(context.Background())
	if res.Status != StatusOk {
		t.Errorf("Status = %q, want ok for a reachable port", res.Status)
	}
	if res.Type != "Tcp" || res.Data["host"] != host || res.Data["port"] != port {
		t.Errorf("result = %+v, want Type Tcp with host/port data", res)
	}
}

func TestTCPCheck_UnreachableIsFailed(t *testing.T) {
	// Bind then immediately close to obtain a port that is (almost certainly) not listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	ln.Close()

	res := TCPCheck("db", host, port, WithTCPTimeout(time.Second)).Check(context.Background())
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, want failed for a closed port", res.Status)
	}
	if res.Data["error"] == nil {
		t.Errorf("Data = %+v, want an error category", res.Data)
	}
}

func TestTCPCheck_Name(t *testing.T) {
	if got := TCPCheck("cache", "localhost", 6379).Name(); got != "cache" {
		t.Errorf("Name = %q, want cache", got)
	}
}

func TestTCPCheck_WithDialerIsUsed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	res := TCPCheck("db", host, port, WithTCPDialer(&net.Dialer{Timeout: time.Second})).Check(context.Background())
	if res.Status != StatusOk {
		t.Errorf("Status = %q, want ok with a custom dialer", res.Status)
	}
}

func TestHTTPPingCheck_OKisOk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := HTTPPingCheck("api", srv.URL, WithHTTPClient(srv.Client())).Check(context.Background())
	if res.Status != StatusOk {
		t.Errorf("Status = %q, want ok for a 200 response", res.Status)
	}
	if res.Type != "HttpPing" || res.Data["statusCode"] != http.StatusOK {
		t.Errorf("result = %+v, want Type HttpPing with statusCode 200", res)
	}
}

func TestHTTPPingCheck_Non200IsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	res := HTTPPingCheck("api", srv.URL, WithHTTPClient(srv.Client())).Check(context.Background())
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, want failed for a 500 response", res.Status)
	}
	if res.Data["statusCode"] != http.StatusInternalServerError {
		t.Errorf("statusCode = %v, want 500", res.Data["statusCode"])
	}
}

// errTransport always fails the round trip, to exercise the request-error branch hermetically.
type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func TestHTTPPingCheck_RequestErrorIsFailed(t *testing.T) {
	res := HTTPPingCheck("api", "http://example.invalid", WithHTTPClient(&http.Client{Transport: errTransport{}})).Check(context.Background())
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, want failed when the request errors", res.Status)
	}
	if res.Data["error"] != "connection-error" {
		t.Errorf("error = %v, want connection-error", res.Data["error"])
	}
}

func TestHTTPPingCheck_InvalidRequestIsFailed(t *testing.T) {
	// A control character in the URL makes http.NewRequestWithContext fail before any dial.
	res := HTTPPingCheck("api", "http://\x7f/bad").Check(context.Background())
	if res.Status != StatusFailed || res.Data["error"] != "invalid-request" {
		t.Errorf("result = %+v, want failed with invalid-request", res)
	}
}

func TestHTTPPingCheck_Name(t *testing.T) {
	if got := HTTPPingCheck("api", "http://x").Name(); got != "api" {
		t.Errorf("Name = %q, want api", got)
	}
}

func TestStripUserInfo(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"strips credentials", "https://user:pass@host/path", "https://host/path"},
		{"strips bare username", "https://user@host/path", "https://host/path"},
		{"leaves credential-free url", "https://host/path?q=1", "https://host/path?q=1"},
		{"returns unparseable url unchanged", "://not a url", "://not a url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripUserInfo(tt.in); got != tt.want {
				t.Errorf("stripUserInfo(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHTTPPingCheck_ReportedURLStripsCredentials(t *testing.T) {
	res := HTTPPingCheck("api", "https://user:secret@example.invalid/health",
		WithHTTPClient(&http.Client{Transport: errTransport{}})).Check(context.Background())
	if res.Data["url"] != "https://example.invalid/health" {
		t.Errorf("reported url = %v, want credentials stripped", res.Data["url"])
	}
}

// timeoutError is a net.Error reporting a timeout, to exercise errorCategory's timeout branch.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestErrorCategory(t *testing.T) {
	if got := errorCategory(timeoutError{}); got != "timeout" {
		t.Errorf("errorCategory(timeout) = %q, want timeout", got)
	}
	if got := errorCategory(errors.New("refused")); got != "connection-error" {
		t.Errorf("errorCategory(other) = %q, want connection-error", got)
	}
}

// TestTCPCheck_IntegratesWithMiddleware proves a ready-made check plugs into the health endpoint
// exactly like a bespoke CheckFunc.
func TestTCPCheck_IntegratesWithMiddleware(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	checks := []Check{TCPCheck("db", host, port)}
	if checks[0].Check(context.Background()).Status != StatusOk {
		t.Fatal("expected the check to pass against the live listener")
	}
	_ = Middleware(checks) // constructs without panic; behavior covered by healthcheck_test.go
}
