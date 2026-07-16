package client

import (
	"context"
	"encoding/json"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// RetryOptions configures RetryDecorator.
type RetryOptions struct {
	// MaxAttempts is the total number of attempts, including the first. Defaults to 3 when
	// <= 0.
	MaxAttempts int
	// Backoff computes the delay before the given attempt number (1-based: the delay before
	// the *second* attempt is Backoff(1)). Defaults to exponential backoff starting at 100ms
	// when nil.
	Backoff func(attempt int) time.Duration
}

// RetryDecorator wraps next, retrying a Send whose Result carries StatusServiceUnavailable -
// wire-contracts.md §3's own description of that status: "Transient infrastructure failure;
// retryable" - up to opts.MaxAttempts times, waiting opts.Backoff between attempts. Any other
// outcome (success, or a different failure status) is returned immediately without retrying,
// since only ServiceUnavailable is defined as transient. A context cancellation during the
// backoff wait returns the last-seen result immediately rather than retrying further.
func RetryDecorator(next Sender, opts RetryOptions) Sender {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	backoff := opts.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}

	return SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		attempt := 0
		for {
			attempt++
			result := next.Send(ctx, topic, headers, message)
			if result.Status != benzene.StatusServiceUnavailable || attempt == maxAttempts {
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

func defaultBackoff(attempt int) time.Duration {
	return time.Duration(100*(1<<uint(attempt-1))) * time.Millisecond
}
