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

// CorrelationDecorator wraps next so an outbound Send propagates an x-correlation-id header
// (wire-contracts.md §2) - the value the caller already put in its own headers (matched
// case-insensitively, per §2's "case-insensitive on read"), left untouched and never
// overwritten. Per §2 the header is "written ... when the application populates one," and
// Benzene "neither requires nor fabricates" a correlation value, so with generate == nil the
// decorator does NOT invent one when the caller's headers lack it - a fresh per-send random
// value correlates nothing and would misrepresent the framework as the source. Pass a non-nil
// generate to opt into producing a value at the edge (e.g. to start a new correlation chain);
// client.RandomCorrelationID is a ready-made generator for that.
func CorrelationDecorator(next Sender, generate func() string) Sender {
	return SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return next.Send(ctx, topic, withCorrelationID(headers, generate), message)
	})
}

func withCorrelationID(headers map[string]string, generate func() string) map[string]string {
	for key := range headers {
		if strings.EqualFold(key, correlationIDHeader) {
			return headers // already populated - never overwrite
		}
	}
	if generate == nil {
		return headers // do not fabricate a correlation value that wasn't there (§2)
	}

	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out[correlationIDHeader] = generate()
	return out
}

// RandomCorrelationID returns a random 32-character hex string, the opt-in edge generator for
// CorrelationDecorator: pass it as generate to start a new correlation chain when the caller did
// not supply one. It is not a UUID (this repo adds no dependency to format one per RFC 9562) but
// is sufficiently unique and URL/header-safe. crypto/rand.Read reading the OS CSPRNG is not
// documented to fail on any platform this library targets, so its error is intentionally ignored
// rather than given a fallback path that would be untestable and never actually taken.
func RandomCorrelationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
