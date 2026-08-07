package healthcheck_test

import (
	"context"
	"strings"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/httpstatus"
	"github.com/daniellepelley/benzene-go/wire"
)

// TestHealthEndToEnd_UnhealthyMapsTo503WithReportBody proves the whole chain an HTTP probe
// sees: an unhealthy check produces a service-unavailable result that httpstatus maps to
// HTTP 503 (so a load balancer drains the instance) while the report body still renders
// (isHealthy:false), not an error payload. Healthy stays 200.
func TestHealthEndToEnd_UnhealthyMapsTo503WithReportBody(t *testing.T) {
	failing := healthcheck.CheckFunc{CheckName: "db", Fn: func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusFailed, Type: "database"}
	}}
	ok := healthcheck.CheckFunc{CheckName: "db", Fn: func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "database"}
	}}

	dispatch := func(checks ...healthcheck.Check) wire.Response {
		container := benzene.NewContainer()
		pipeline := benzene.NewPipeline(
			healthcheck.Middleware(checks),
			benzene.RouterMiddleware(benzene.NewRegistry()),
		)
		return envelope.Dispatch(context.Background(), pipeline, container, wire.Request{
			Topic: healthcheck.ReservedTopic, Headers: map[string]string{}, Body: "",
		})
	}

	unhealthy := dispatch(failing)
	if unhealthy.StatusCode != string(benzene.StatusServiceUnavailable) {
		t.Errorf("unhealthy StatusCode = %q, want %q", unhealthy.StatusCode, benzene.StatusServiceUnavailable)
	}
	if code := httpstatus.ToHTTP(benzene.Status(unhealthy.StatusCode)); code != 503 {
		t.Errorf("unhealthy HTTP code = %d, want 503", code)
	}
	if !strings.Contains(unhealthy.Body, `"isHealthy":false`) {
		t.Errorf("unhealthy body = %q, want the health report with isHealthy:false (not an error payload)", unhealthy.Body)
	}

	healthy := dispatch(ok)
	if healthy.StatusCode != string(benzene.StatusOk) {
		t.Errorf("healthy StatusCode = %q, want %q", healthy.StatusCode, benzene.StatusOk)
	}
	if code := httpstatus.ToHTTP(benzene.Status(healthy.StatusCode)); code != 200 {
		t.Errorf("healthy HTTP code = %d, want 200", code)
	}
}
