package ratelimiting_test

import (
	"context"
	"fmt"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/ratelimiting"
)

// ExampleMiddleware shows per-instance rate limiting. A message that can acquire its permit cost from
// the limiter runs the handler; one that can't short-circuits to too-many-requests without running it.
// Here a token bucket with a burst of 1 (and a frozen clock, so nothing refills) admits the first
// message and rejects the second.
func ExampleMiddleware() {
	now := time.Unix(0, 0)
	limiter := ratelimiting.NewTokenBucket(1, 1, ratelimiting.WithClock(func() time.Time { return now }))
	mw := ratelimiting.Middleware(limiter, ratelimiting.OnePerMessage)

	run := func() benzene.Status {
		ic := benzene.NewInvocationContext(benzene.NewTopic("t"), nil, nil, nil)
		_ = mw(context.Background(), ic, func(context.Context) error {
			ic.Result = benzene.Ok(struct{}{})
			return nil
		})
		return ic.Result.ResultStatus()
	}

	fmt.Println("1st:", run())
	fmt.Println("2nd:", run())
	// Output:
	// 1st: ok
	// 2nd: too-many-requests
}
