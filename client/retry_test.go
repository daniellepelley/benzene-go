package client

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

func countingSender(statuses ...benzene.Status) (Sender, func() int) {
	var calls int32
	sender := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		i := atomic.AddInt32(&calls, 1) - 1
		status := statuses[len(statuses)-1]
		if int(i) < len(statuses) {
			status = statuses[i]
		}
		return benzene.Result[json.RawMessage]{Status: status}
	})
	return sender, func() int { return int(atomic.LoadInt32(&calls)) }
}

func noBackoff(int) time.Duration { return 0 }

func TestRetryDecorator_SuccessOnFirstAttemptDoesNotRetry(t *testing.T) {
	sender, calls := countingSender(benzene.StatusOk)
	decorated := RetryDecorator(sender, RetryOptions{Backoff: noBackoff})

	result := decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if result.Status != benzene.StatusOk {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if calls() != 1 {
		t.Errorf("calls = %d, want 1", calls())
	}
}

func TestRetryDecorator_RetriesUntilSuccess(t *testing.T) {
	sender, calls := countingSender(benzene.StatusServiceUnavailable, benzene.StatusServiceUnavailable, benzene.StatusOk)
	decorated := RetryDecorator(sender, RetryOptions{MaxAttempts: 5, Backoff: noBackoff})

	result := decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if result.Status != benzene.StatusOk {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if calls() != 3 {
		t.Errorf("calls = %d, want 3", calls())
	}
}

func TestRetryDecorator_ExhaustsAllAttemptsReturnsFinalFailure(t *testing.T) {
	sender, calls := countingSender(benzene.StatusServiceUnavailable)
	decorated := RetryDecorator(sender, RetryOptions{MaxAttempts: 3, Backoff: noBackoff})

	result := decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
	if calls() != 3 {
		t.Errorf("calls = %d, want 3 (MaxAttempts)", calls())
	}
}

func TestRetryDecorator_NonRetryableStatusIsNotRetried(t *testing.T) {
	sender, calls := countingSender(benzene.StatusBadRequest)
	decorated := RetryDecorator(sender, RetryOptions{MaxAttempts: 5, Backoff: noBackoff})

	result := decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if result.Status != benzene.StatusBadRequest {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusBadRequest)
	}
	if calls() != 1 {
		t.Errorf("calls = %d, want 1 - only ServiceUnavailable is retryable", calls())
	}
}

func TestRetryDecorator_DefaultMaxAttemptsIsThree(t *testing.T) {
	sender, calls := countingSender(benzene.StatusServiceUnavailable)
	decorated := RetryDecorator(sender, RetryOptions{Backoff: noBackoff})

	decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if calls() != 3 {
		t.Errorf("calls = %d, want 3 (default MaxAttempts)", calls())
	}
}

func TestRetryDecorator_NegativeMaxAttemptsUsesDefault(t *testing.T) {
	sender, calls := countingSender(benzene.StatusServiceUnavailable)
	decorated := RetryDecorator(sender, RetryOptions{MaxAttempts: -1, Backoff: noBackoff})

	decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if calls() != 3 {
		t.Errorf("calls = %d, want 3 (default MaxAttempts)", calls())
	}
}

func TestRetryDecorator_ContextCancelledDuringBackoffReturnsEarly(t *testing.T) {
	sender, calls := countingSender(benzene.StatusServiceUnavailable)
	decorated := RetryDecorator(sender, RetryOptions{MaxAttempts: 10, Backoff: func(int) time.Duration { return time.Hour }})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan benzene.Result[json.RawMessage], 1)
	go func() {
		done <- decorated.Send(ctx, benzene.NewTopic("t"), nil, nil)
	}()

	// Give the first attempt time to run and enter the (hour-long) backoff wait, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if result.Status != benzene.StatusServiceUnavailable {
			t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RetryDecorator did not return promptly after context cancellation")
	}
	if calls() != 1 {
		t.Errorf("calls = %d, want 1 - cancellation during backoff should prevent a second attempt", calls())
	}
}

func TestRetryDecorator_NilBackoffUsesDefault(t *testing.T) {
	sender, calls := countingSender(benzene.StatusServiceUnavailable, benzene.StatusOk)
	start := time.Now()
	decorated := RetryDecorator(sender, RetryOptions{MaxAttempts: 2})

	decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if calls() != 2 {
		t.Errorf("calls = %d, want 2", calls())
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 100ms (the default backoff before attempt 2)", elapsed)
	}
}

func TestDefaultBackoff_IsExponential(t *testing.T) {
	if defaultBackoff(1) != 100*time.Millisecond {
		t.Errorf("defaultBackoff(1) = %v, want 100ms", defaultBackoff(1))
	}
	if defaultBackoff(2) != 200*time.Millisecond {
		t.Errorf("defaultBackoff(2) = %v, want 200ms", defaultBackoff(2))
	}
	if defaultBackoff(3) != 400*time.Millisecond {
		t.Errorf("defaultBackoff(3) = %v, want 400ms", defaultBackoff(3))
	}
}
