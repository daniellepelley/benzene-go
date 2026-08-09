package client

import (
	"context"
	"encoding/json"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// RetryOptions configures WithRetry.
type RetryOptions struct {
	// MaxAttempts is the total number of attempts, including the first (so 3 means one initial
	// try plus up to two retries). Defaults to 3 when <= 0.
	MaxAttempts int
	// Backoff computes the delay before the given attempt number (1-based: the delay before
	// the *second* attempt is Backoff(1)). Defaults to exponential backoff starting at 100ms
	// when nil.
	Backoff func(attempt int) time.Duration
	// ShouldRetry decides whether a result's status warrants another attempt. Defaults to the
	// spec's transient-and-retry-safe set - StatusServiceUnavailable and StatusTooManyRequests -
	// when nil. StatusTimeout is deliberately NOT in the default set: it is transient, but a
	// timeout leaves it unknown whether the operation was applied, so a blind retry is only safe
	// for idempotent operations (wire-contracts.md §3); a caller that knows its operation is
	// idempotent can opt in via a custom predicate rather than a fixed baked-in status set.
	ShouldRetry func(status benzene.Status) bool
}

// WithRetry wraps next, retrying a Send whose Result status opts.ShouldRetry accepts - by
// default the spec's transient-and-retry-safe statuses StatusServiceUnavailable ("Transient
// infrastructure failure; retryable") and StatusTooManyRequests ("Throttled / rate limited;
// transient - back off and retry"), per wire-contracts.md §3 - up to opts.MaxAttempts times,
// waiting opts.Backoff between attempts. Any other outcome (success, or a non-retryable failure)
// is returned immediately. A context cancellation during the backoff wait returns the last-seen
// result immediately rather than retrying further.
func WithRetry(next Sender, opts RetryOptions) Sender {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	backoff := opts.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	shouldRetry := opts.ShouldRetry
	if shouldRetry == nil {
		shouldRetry = defaultShouldRetry
	}

	return SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		attempt := 0
		for {
			attempt++
			result := next.Send(ctx, topic, headers, message)
			if !shouldRetry(result.Status) || attempt == maxAttempts {
				return result
			}

			select {
			case <-ctx.Done():
				return result
			case <-time.After(backoff(attempt)):
			}
		}
	})
}

// defaultShouldRetry is the spec's transient-and-retry-safe status set (wire-contracts.md §3):
// service-unavailable and too-many-requests. timeout is excluded - see RetryOptions.ShouldRetry.
func defaultShouldRetry(status benzene.Status) bool {
	return status == benzene.StatusServiceUnavailable || status == benzene.StatusTooManyRequests
}

func defaultBackoff(attempt int) time.Duration {
	return time.Duration(100*(1<<uint(attempt-1))) * time.Millisecond
}
