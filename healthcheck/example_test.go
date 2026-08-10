package healthcheck_test

import (
	"context"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/healthcheck"
)

// ExampleMiddleware shows the health-check endpoint. Register the checks a service depends on
// (NamedCheck adapts a plain function; TCPCheck/HTTPPingCheck/DiskSpaceCheck are ready-made), wrap
// them as middleware, and a message on the reserved benzene:healthcheck topic is answered with the
// aggregate report without ever reaching a handler. A transport binding mounts this at
// httpbinding.HealthPath ("/benzene/health"); here we drive the pipeline directly.
func ExampleMiddleware() {
	checks := []healthcheck.Check{
		healthcheck.NamedCheck("db", func(context.Context) healthcheck.CheckResult {
			return healthcheck.CheckResult{Status: healthcheck.StatusOk, Type: "db"}
		}),
	}
	pipeline := benzene.NewPipeline(healthcheck.Middleware(checks))

	ic := benzene.NewInvocationContext(benzene.NewTopic(healthcheck.ReservedTopic), nil, nil, nil)
	if err := pipeline.Run(context.Background(), ic); err != nil {
		panic(err)
	}

	report := ic.Result.ResultPayload().(healthcheck.Response)
	fmt.Println("status:", ic.Result.ResultStatus())
	fmt.Println("healthy:", report.IsHealthy)
	fmt.Println("db:", report.HealthChecks["db"].Status)
	// Output:
	// status: ok
	// healthy: true
	// db: ok
}
