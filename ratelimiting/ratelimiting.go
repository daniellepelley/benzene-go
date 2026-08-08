// Package ratelimiting is a best-effort, per-instance rate-limiting middleware: each message tries
// to acquire its permit cost from a Limiter without queuing, and a message the limiter rejects is
// short-circuited with a too-many-requests result (HTTP 429 via the standard status mapping) before
// the handler runs.
//
// It is the Go-idiomatic form of Benzene.RateLimiting. The .NET package leans on
// System.Threading.RateLimiting (a dependency); this port keeps the root module dependency-free by
// defining a small Limiter interface and shipping a standard-library TokenBucket as the default -
// an application that wants a different algorithm (a sliding window, a distributed limiter) plugs it
// in behind the same interface, e.g. a thin adapter over golang.org/x/time/rate, without this
// package taking the dependency.
//
// This is deliberately simple protection for endpoints a service cannot avoid exposing - a brake on
// abuse and runaway serverless cost, not an exact science: the limit is per service instance, so a
// fleet of N instances admits up to N times the configured rate. Authoritative rate limiting belongs
// at the gateway in front of all instances.
package ratelimiting

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// Limiter decides whether a message may proceed now, given its permit cost. Allow returns a release
// function to call when the message completes and whether the permit was granted. release is
// meaningful only when ok is true: it is a no-op for rate/window limiters (a token bucket does not
// return spent tokens) and returns the permit for a concurrency-style limiter; the middleware holds
// it across the handler so a concurrency limiter releases correctly. Implementations MUST be safe
// for concurrent use.
type Limiter interface {
	Allow(cost int) (release func(), ok bool)
}

// CostFunc computes the permit cost of an invocation - 1 for a plain request-rate limit, or e.g. the
// payload size for a bytes-per-second bucket. A negative cost is clamped to 0 (free).
type CostFunc func(ic *benzene.InvocationContext) int

// OnePerMessage is the default CostFunc: every message costs one permit.
func OnePerMessage(*benzene.InvocationContext) int { return 1 }

// Middleware returns the rate-limiting middleware over limiter, costing each message with cost. A
// rejected message short-circuits to benzene.TooManyRequests without running the handler; an allowed
// message runs the rest of the pipeline with the lease held across it (released when it completes).
// Register it early - before the router/handler, typically after logging so throttled messages are
// still observed.
func Middleware(limiter Limiter, cost CostFunc) benzene.Middleware {
	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		c := cost(ic)
		if c < 0 {
			c = 0
		}
		release, ok := limiter.Allow(c)
		if !ok {
			ic.Result = benzene.TooManyRequests[any]("rate limit exceeded")
			return nil // short-circuit: the router/handler downstream do not run
		}
		defer release()
		return next(ctx)
	}
}
