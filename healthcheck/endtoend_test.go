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
// sees: an unhealthy check produces an ok result carrying isHealthy:false (wire-contracts.md §5),
// which httpstatus maps to HTTP 503 on a health route (so a load balancer drains the instance)
// with the report as the body, not an error payload. Healthy stays 200.
func TestHealthEndToEnd_UnhealthyMapsTo503WithReportBody(t *testing.T) {
	failing := healthcheck.NamedCheck("db", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusFailed, Type: "database"}
	})
	ok := healthcheck.NamedCheck("db", func(context.Context) healthcheck.CheckResult {
		return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "database"}
	})

	topic := benzene.NewTopic(healthcheck.ReservedTopic)
	dispatch := func(checks ...healthcheck.Check) wire.Response {
		container := benzene.NewContainer()
		pipeline := benzene.NewPipeline(
			healthcheck.Middleware(checks),
			benzene.RouterMiddleware(benzene.NewRegistry()),
		)
		return envelope.Dispatch(context.Background(), pipeline, container, wire.Request{
			Topic: topic.ID, Headers: map[string]string{}, Body: "",
		})
	}

	unhealthy := dispatch(failing)
	if unhealthy.StatusCode != string(benzene.StatusOk) {
		t.Errorf("unhealthy StatusCode = %q, want %q", unhealthy.StatusCode, benzene.StatusOk)
	}
	code, body, headers := httpstatus.Response(topic, unhealthy)
	if code != 503 {
		t.Errorf("unhealthy HTTP code = %d, want 503", code)
	}
	if !strings.Contains(body, `"isHealthy":false`) || headers["content-type"] != "application/json" {
		t.Errorf("unhealthy body = %q (%v), want the health report with isHealthy:false (not an error payload)", body, headers)
	}

	healthy := dispatch(ok)
	if healthy.StatusCode != string(benzene.StatusOk) {
		t.Errorf("healthy StatusCode = %q, want %q", healthy.StatusCode, benzene.StatusOk)
	}
	if code, _, _ := httpstatus.Response(topic, healthy); code != 200 {
		t.Errorf("healthy HTTP code = %d, want 200", code)
	}
}
