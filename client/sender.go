// Package client provides the outbound-client decorators of
// daniellepelley/Benzene's docs/specification/transport-bindings.md §2: "Cross-cutting client
// behaviors (correlation ID injection, trace context, retry) are decorators over the same
// interface and therefore transport-agnostic." Sender is that one interface;
// CorrelationDecorator and RetryDecorator are decorators over it - each wraps a Sender and
// returns another Sender, so they compose freely and work over any transport's outbound client
// (httpclient.Client already satisfies Sender structurally, with no changes needed there).
package client

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
)

// Sender is the outbound-client contract every transport's client implements: send a message
// to topic with headers, get back a Result carrying the raw response payload. A caller uses
// client's own Unmarshal-style helper (see httpclient.Unmarshal) to convert the payload into
// an application type, keeping this interface transport-agnostic - it never names a concrete
// response type.
type Sender interface {
	Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage]
}

// SenderFunc adapts a plain function to a Sender.
type SenderFunc func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage]

// Send calls f.
func (f SenderFunc) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	return f(ctx, topic, headers, message)
}
