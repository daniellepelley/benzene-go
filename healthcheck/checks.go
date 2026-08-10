package healthcheck

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file holds ready-made Check implementations for probing a downstream dependency's
// reachability, the Go equivalents of Benzene.HealthChecks.Tcp / .Http. They are zero-dependency
// (net / net/http) and each implements the package's Check interface, so they plug straight into
// Middleware alongside any bespoke CheckFunc. (The host self-check on free space,
// Benzene.HealthChecks.Disk, lives in disk.go - also zero-dependency, with the one platform-specific
// call behind build tags.)

// TCPCheck verifies a dependency is reachable at the TCP (L4) level by opening a connection to
// host:port - the lowest-common-denominator check for anything without a first-class client (a
// database port, an SMTP server, a custom service). It reports StatusOk when the connection is
// accepted and StatusFailed on any dial error (an expected "connection refused" is a failed result,
// not a panic). Matches Benzene.HealthChecks.Tcp. name identifies the check in the response map; the
// reported CheckResult.Type is "Tcp". The failure Data carries a coarse error CATEGORY
// ("timeout"/"connection-error"), never the raw error message, which can carry infrastructure detail
// that would flow out to an unauthenticated health-check caller.
func TCPCheck(name, host string, port int, opts ...TCPOption) Check {
	cfg := tcpConfig{dialer: &net.Dialer{}}
	for _, o := range opts {
		o(&cfg)
	}
	return tcpCheck{name: name, host: host, port: port, cfg: cfg}
}

// TCPOption configures a TCPCheck.
type TCPOption func(*tcpConfig)

type tcpConfig struct {
	dialer  *net.Dialer
	timeout time.Duration
}

// WithTCPTimeout bounds the dial with a per-check deadline, so a hung dependency cannot stall the
// health endpoint. Default: none (only the invocation context's own deadline, if any, applies).
func WithTCPTimeout(d time.Duration) TCPOption { return func(c *tcpConfig) { c.timeout = d } }

// WithTCPDialer overrides the net.Dialer used for the connection (e.g. to bind a source address or
// set keep-alive). Default: a zero-value net.Dialer.
func WithTCPDialer(d *net.Dialer) TCPOption { return func(c *tcpConfig) { c.dialer = d } }

type tcpCheck struct {
	name string
	host string
	port int
	cfg  tcpConfig
}

func (c tcpCheck) Name() string { return c.name }

func (c tcpCheck) Check(ctx context.Context) CheckResult {
	if c.cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.timeout)
		defer cancel()
	}
	data := map[string]any{"host": c.host, "port": c.port}
	conn, err := c.cfg.dialer.DialContext(ctx, "tcp", net.JoinHostPort(c.host, strconv.Itoa(c.port)))
	if err != nil {
		data["error"] = errorCategory(err)
		return CheckResult{Status: StatusFailed, Type: "Tcp", Data: data}
	}
	_ = conn.Close()
	return CheckResult{Status: StatusOk, Type: "Tcp", Data: data}
}

// HTTPPingCheck checks a downstream HTTP dependency by issuing a GET and treating a 200 OK as
// healthy - any other status code (including non-2xx success codes) is StatusFailed. It uses the
// injected *http.Client's own timeout/transport (this type adds no retry of its own), matching
// Benzene.HealthChecks.Http's reliance on the injected HttpClient. name identifies the check; the
// reported Type is "HttpPing". The Data always carries the checked url (with any userinfo/basic-auth
// credentials stripped - the reported url flows out to whoever calls the health topic) and, on a
// response, the statusCode.
func HTTPPingCheck(name, rawURL string, opts ...HTTPOption) Check {
	cfg := httpConfig{client: http.DefaultClient}
	for _, o := range opts {
		o(&cfg)
	}
	return httpPingCheck{name: name, url: rawURL, cfg: cfg}
}

// HTTPOption configures an HTTPPingCheck.
type HTTPOption func(*httpConfig)

type httpConfig struct {
	client *http.Client
}

// WithHTTPClient overrides the *http.Client used for the ping - the place to set a timeout, a custom
// transport, or TLS config. Default: http.DefaultClient (no timeout, so set one for production use).
func WithHTTPClient(client *http.Client) HTTPOption { return func(c *httpConfig) { c.client = client } }

type httpPingCheck struct {
	name string
	url  string
	cfg  httpConfig
}

func (c httpPingCheck) Name() string { return c.name }

func (c httpPingCheck) Check(ctx context.Context) CheckResult {
	reported := stripUserInfo(c.url)
	data := map[string]any{"url": reported}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		data["error"] = "invalid-request"
		return CheckResult{Status: StatusFailed, Type: "HttpPing", Data: data}
	}
	resp, err := c.cfg.client.Do(req)
	if err != nil {
		data["error"] = errorCategory(err)
		return CheckResult{Status: StatusFailed, Type: "HttpPing", Data: data}
	}
	defer resp.Body.Close()

	data["statusCode"] = resp.StatusCode
	status := StatusFailed
	if resp.StatusCode == http.StatusOK {
		status = StatusOk
	}
	return CheckResult{Status: status, Type: "HttpPing", Data: data}
}

// errorCategory maps a dial/request error to a coarse, non-sensitive category. The raw message is
// deliberately not surfaced (it can carry internal hostnames/paths), matching the .NET checks'
// choice to report the exception type rather than its message.
func errorCategory(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "connection-error"
}

// stripUserInfo removes any userinfo (basic-auth credentials) from a URL before it is reported, so a
// "https://user:pass@host" dependency URL does not leak its credentials to a health-check caller. The
// request itself always uses the original URL. A URL that does not parse still gets a manual
// authority strip (below) rather than being echoed verbatim - credential-stripping is a security
// property, so the fail-safe must never surface the raw credential-bearing string.
func stripUserInfo(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		if u.User == nil {
			return rawURL
		}
		u.User = nil
		return u.String()
	}
	return stripUserInfoFallback(rawURL)
}

// stripUserInfoFallback removes a "userinfo@" prefix from the authority of a URL that url.Parse
// rejected (e.g. an invalid port), operating only on the text between "://" and the first "/","?" or
// "#". It uses the LAST "@" in the authority (a host cannot contain one), so credentials are dropped
// even when the rest of the URL is malformed; a URL with no authority or no userinfo is returned
// unchanged.
func stripUserInfoFallback(rawURL string) string {
	scheme := strings.Index(rawURL, "://")
	if scheme < 0 {
		return rawURL
	}
	start := scheme + len("://")
	end := start + strings.IndexAny(rawURL[start:], "/?#")
	if end < start {
		end = len(rawURL)
	}
	authority := rawURL[start:end]
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return rawURL
	}
	return rawURL[:start] + authority[at+1:] + rawURL[end:]
}
