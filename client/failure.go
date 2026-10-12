package client

import (
	"context"
	"encoding/json"
	"errors"

	benzene "github.com/daniellepelley/benzene-go"
)

// CallFailure is the Result a reply-receiving Sender returns when its call fails before any reply
// arrives - no connection, a refused or reset request, the caller's own deadline
// (transport-bindings.md §1: such a call SHOULD yield a result rather than an exception, so a
// caller handles a dead peer as it handles every other failure). It is StatusTimeout when the
// deadline elapsed and StatusServiceUnavailable otherwise, with detail as its one error.
//
// The deadline is taken to have elapsed when ctx's own deadline has passed, when err wraps
// context.DeadlineExceeded, or when err reports itself a timeout (a net.Error, or an
// http.Client.Timeout, whose Timeout method says so). A cancelled context is not a deadline: it
// is service-unavailable, like every other failure that is not a timeout.
func CallFailure(ctx context.Context, err error, detail string) benzene.Result[json.RawMessage] {
	if DeadlineElapsed(ctx, err) {
		return benzene.Timeout[json.RawMessage](detail)
	}
	return benzene.ServiceUnavailable[json.RawMessage](detail)
}

// DeadlineElapsed reports whether a call that failed with err failed because a deadline elapsed:
// ctx's deadline has passed, err wraps context.DeadlineExceeded, or err (or anything it wraps)
// has a Timeout method that returns true. It is the test behind CallFailure, exported for a
// Sender that maps the native failure itself.
func DeadlineElapsed(ctx context.Context, err error) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
