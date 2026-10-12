package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// timeoutError is a net.Error-shaped failure (what net/http's Client.Timeout and a dial timeout
// report): it says it is a timeout without wrapping context.DeadlineExceeded.
type timeoutError struct{ timeout bool }

func (e timeoutError) Error() string { return "i/o timeout" }
func (e timeoutError) Timeout() bool { return e.timeout }

// TestCallFailure pins transport-bindings.md §1: a call that fails before any reply yields a
// Result - timeout when the deadline elapsed, otherwise service-unavailable.
func TestCallFailure(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want benzene.Status
	}{
		{"the caller's deadline has passed", expired, errors.New("dial tcp: operation was canceled"), benzene.StatusTimeout},
		{"the error wraps context.DeadlineExceeded", context.Background(), fmt.Errorf("invoke: %w", context.DeadlineExceeded), benzene.StatusTimeout},
		{"the error says it is a timeout", context.Background(), fmt.Errorf("Post: %w", timeoutError{timeout: true}), benzene.StatusTimeout},
		{"a net error that is not a timeout", context.Background(), timeoutError{timeout: false}, benzene.StatusServiceUnavailable},
		{"a refused connection", context.Background(), errors.New("connect: connection refused"), benzene.StatusServiceUnavailable},
		{"a cancelled call is not a deadline", cancelled, context.Canceled, benzene.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CallFailure(tt.ctx, tt.err, "call failed")
			if result.Status != tt.want {
				t.Errorf("Status = %q, want %q", result.Status, tt.want)
			}
			if result.IsSuccessful() {
				t.Error("IsSuccessful() = true, want a failed result")
			}
			if len(result.Errors) != 1 || result.Errors[0].Message != "call failed" {
				t.Errorf("Errors = %v, want the detail as one error", result.Errors)
			}
		})
	}
}
