package healthcheck

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func okCheck(name string) Check {
	return CheckFunc{CheckName: name, Fn: func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusOk, Type: "test", Data: map[string]any{"ok": true}}
	}}
}

func runMiddleware(t *testing.T, checks []Check, topic string, aliases ...string) (*benzene.InvocationContext, bool) {
	t.Helper()
	nextCalled := false
	next := func(context.Context) error {
		nextCalled = true
		return nil
	}
	ic := benzene.NewInvocationContext(benzene.NewTopic(topic), nil, nil, nil)
	mw := Middleware(checks, aliases...)
	if err := mw(context.Background(), ic, next); err != nil {
		t.Fatalf("middleware error = %v", err)
	}
	return ic, nextCalled
}

func TestMiddleware_NonMatchingTopicPassesThrough(t *testing.T) {
	ic, nextCalled := runMiddleware(t, []Check{okCheck("db")}, "order:create")

	if !nextCalled {
		t.Error("next should be called for a non-healthcheck topic")
	}
	if ic.Result != nil {
		t.Error("ic.Result should remain unset when passing through")
	}
}

func TestMiddleware_AllHealthyChecksAreHealthy(t *testing.T) {
	ic, nextCalled := runMiddleware(t, []Check{okCheck("db"), okCheck("cache")}, "healthcheck")

	if nextCalled {
		t.Error("next should not be called - healthcheck must short-circuit")
	}
	resp := resultPayload(t, ic)
	if !resp.IsHealthy {
		t.Error("IsHealthy = false, want true")
	}
	if len(resp.HealthChecks) != 2 {
		t.Errorf("len(HealthChecks) = %d, want 2", len(resp.HealthChecks))
	}
	if resp.HealthChecks["db"].Status != StatusOk {
		t.Errorf(`HealthChecks["db"].Status = %q, want %q`, resp.HealthChecks["db"].Status, StatusOk)
	}
}

func TestMiddleware_FailedCheckMakesResponseUnhealthy(t *testing.T) {
	failing := CheckFunc{CheckName: "db", Fn: func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusFailed, Type: "database", Data: map[string]any{"CanConnect": false}}
	}}
	ic, _ := runMiddleware(t, []Check{okCheck("cache"), failing}, "healthcheck")

	resp := resultPayload(t, ic)
	if resp.IsHealthy {
		t.Error("IsHealthy = true, want false when any check failed")
	}
	if resp.HealthChecks["db"].Status != StatusFailed {
		t.Errorf(`HealthChecks["db"].Status = %q, want %q`, resp.HealthChecks["db"].Status, StatusFailed)
	}
}

func TestMiddleware_WarningDoesNotFlipHealthy(t *testing.T) {
	warning := CheckFunc{CheckName: "queue", Fn: func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusWarning, Type: "queue"}
	}}
	ic, _ := runMiddleware(t, []Check{okCheck("db"), warning}, "healthcheck")

	resp := resultPayload(t, ic)
	if !resp.IsHealthy {
		t.Error("IsHealthy = false, want true - a warning must not flip the aggregate")
	}
}

func TestMiddleware_PanickingCheckIsRecordedAsFailed(t *testing.T) {
	panicking := CheckFunc{CheckName: "broken", Fn: func(ctx context.Context) CheckResult {
		panic("boom")
	}}
	ic, _ := runMiddleware(t, []Check{panicking}, "healthcheck")

	resp := resultPayload(t, ic)
	if resp.IsHealthy {
		t.Error("IsHealthy = true, want false when a check panics")
	}
	got := resp.HealthChecks["broken"]
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Data["panic"] != "boom" {
		t.Errorf(`Data["panic"] = %v, want "boom"`, got.Data["panic"])
	}
}

func TestMiddleware_DuplicateNamesAreDeduplicated(t *testing.T) {
	ic, _ := runMiddleware(t, []Check{okCheck("db"), okCheck("db"), okCheck("db")}, "healthcheck")

	resp := resultPayload(t, ic)
	if len(resp.HealthChecks) != 3 {
		t.Fatalf("len(HealthChecks) = %d, want 3", len(resp.HealthChecks))
	}
	for _, key := range []string{"db", "db-2", "db-3"} {
		if _, ok := resp.HealthChecks[key]; !ok {
			t.Errorf("HealthChecks missing deduplicated key %q: %+v", key, resp.HealthChecks)
		}
	}
}

func TestMiddleware_AliasTopicIsAlsoIntercepted(t *testing.T) {
	ic, nextCalled := runMiddleware(t, []Check{okCheck("db")}, "ping", "ping")

	if nextCalled {
		t.Error("next should not be called for an aliased healthcheck topic")
	}
	resultPayload(t, ic)
}

func TestMiddleware_NoChecksIsHealthyWithEmptyMap(t *testing.T) {
	ic, _ := runMiddleware(t, nil, "healthcheck")

	resp := resultPayload(t, ic)
	if !resp.IsHealthy {
		t.Error("IsHealthy = false, want true with zero checks registered")
	}
	if len(resp.HealthChecks) != 0 {
		t.Errorf("len(HealthChecks) = %d, want 0", len(resp.HealthChecks))
	}
}

func resultPayload(t *testing.T, ic *benzene.InvocationContext) Response {
	t.Helper()
	if ic.Result == nil {
		t.Fatal("ic.Result should be set after the healthcheck middleware runs")
	}
	payload, ok := ic.Result.ResultPayload().(Response)
	if !ok {
		t.Fatalf("ic.Result.ResultPayload() type = %T, want healthcheck.Response", ic.Result.ResultPayload())
	}
	return payload
}
