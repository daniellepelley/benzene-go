// Package healthcheck implements the health-check interception feature of
// daniellepelley/Benzene's docs/specification/core-concepts.md §5 ("intercept the reserved
// healthcheck topic (plus an app-chosen alias), run registered checks, respond with the
// standard response format") and the response shape of wire-contracts.md §5.
package healthcheck

import (
	"context"
	"fmt"
	"sync"

	benzene "github.com/daniellepelley/benzene-go"
)

// Status is the per-check status vocabulary of wire-contracts.md §5 - deliberately a
// different, lower-case set from the framework's Status (result.go): "ok", "warning", or
// "failed".
type Status string

const (
	StatusOk      Status = "ok"
	StatusWarning Status = "warning"
	StatusFailed  Status = "failed"
)

// CheckResult is one check's outcome (wire-contracts.md §5). Data is a free-form diagnostic
// bag written verbatim - no naming policy applied.
type CheckResult struct {
	Status Status         `json:"status"`
	Type   string         `json:"type"`
	Data   map[string]any `json:"data,omitempty"`
}

// Check is a single registered health check.
type Check interface {
	// Name identifies this check in the response's healthChecks map. Colliding names across
	// checks are deduplicated with -2/-3 suffixes, in registration order (wire-contracts.md
	// §5).
	Name() string
	Check(ctx context.Context) CheckResult
}

// CheckFunc adapts a name and a plain function into a Check, for callers who don't need a
// dedicated type.
type CheckFunc struct {
	CheckName string
	Fn        func(ctx context.Context) CheckResult
}

func (f CheckFunc) Name() string                          { return f.CheckName }
func (f CheckFunc) Check(ctx context.Context) CheckResult { return f.Fn(ctx) }

// Response is the aggregate health-check response (wire-contracts.md §5).
type Response struct {
	IsHealthy    bool                   `json:"isHealthy"`
	HealthChecks map[string]CheckResult `json:"healthChecks"`
}

// Middleware intercepts the reserved "healthcheck" topic (plus any additional aliases) and
// short-circuits the pipeline (core-concepts.md §5) with the aggregate Response, running
// every check concurrently. A check that panics is recorded as StatusFailed rather than
// crashing the invocation - a health check reporting its own failure is exactly the
// information a caller needs, and one broken check must not take down the whole endpoint.
//
// Any topic other than the reserved one(s) passes through to next unchanged.
func Middleware(checks []Check, aliases ...string) benzene.Middleware {
	topics := make(map[string]bool, len(aliases)+1)
	topics["healthcheck"] = true
	for _, alias := range aliases {
		topics[alias] = true
	}

	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		if !topics[ic.Topic.ID] {
			return next(ctx)
		}

		results := make([]CheckResult, len(checks))
		var wg sync.WaitGroup
		wg.Add(len(checks))
		for i, check := range checks {
			go func(i int, check Check) {
				defer wg.Done()
				results[i] = runCheck(ctx, check)
			}(i, check)
		}
		wg.Wait()

		response := Response{IsHealthy: true, HealthChecks: make(map[string]CheckResult, len(checks))}
		for i, check := range checks {
			key := dedupeKey(response.HealthChecks, check.Name())
			result := results[i]
			response.HealthChecks[key] = result
			if result.Status == StatusFailed {
				response.IsHealthy = false
			}
		}

		ic.Result = benzene.Ok(response)
		return nil
	}
}

func runCheck(ctx context.Context, check Check) (result CheckResult) {
	defer func() {
		if r := recover(); r != nil {
			result = CheckResult{Status: StatusFailed, Type: check.Name(), Data: map[string]any{"panic": fmt.Sprint(r)}}
		}
	}()
	return check.Check(ctx)
}

func dedupeKey(existing map[string]CheckResult, name string) string {
	if _, taken := existing[name]; !taken {
		return name
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", name, n)
		if _, taken := existing[candidate]; !taken {
			return candidate
		}
	}
}
