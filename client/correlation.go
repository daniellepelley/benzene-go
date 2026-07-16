package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
)

// correlationIDHeader is wire-contracts.md §2's outbound correlation header: "Legacy
// correlation value, written by the outbound correlation client decorator when the application
// populates one."
const correlationIDHeader = "x-correlation-id"

// CorrelationDecorator wraps next so every outbound Send carries an x-correlation-id header
// (wire-contracts.md §2), generating one via generate when the caller's own headers don't
// already set it (checked case-insensitively, matching wire-contracts.md §2's "case-insensitive
// on read" rule) - an existing value is left untouched, never overwritten. Pass nil for
// generate to use a default crypto/rand-based generator.
func CorrelationDecorator(next Sender, generate func() string) Sender {
	if generate == nil {
		generate = defaultCorrelationID
	}
	return SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return next.Send(ctx, topic, withCorrelationID(headers, generate), message)
	})
}

func withCorrelationID(headers map[string]string, generate func() string) map[string]string {
	for key := range headers {
		if strings.EqualFold(key, correlationIDHeader) {
			return headers
		}
	}

	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out[correlationIDHeader] = generate()
	return out
}

// defaultCorrelationID returns a random 32-character hex string - not a UUID (this repo adds
// no dependency to format one per RFC 9562), but sufficiently unique and URL/header-safe for a
// correlation value. crypto/rand.Read reading from the OS's CSPRNG is not documented to fail on
// any platform this library targets, so its error is intentionally ignored rather than given a
// fallback path that would be untestable and never actually taken.
func defaultCorrelationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
