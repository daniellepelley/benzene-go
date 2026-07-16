package awslambda

import (
	"context"
	"encoding/json"
	"fmt"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// EnvelopeHandler adapts a HandlerFunc that receives the raw wire-contracts.md envelope
// directly - transport-bindings.md's AWS Lambda catalog entry lists "the raw BenzeneMessage
// envelope for direct invocation" as one of the event shapes a Lambda binding selects between.
// Use this for Lambda-to-Lambda invokes (via httpclient wired to the AWS SDK's Invoke API, or
// any other direct invocation) with no HTTP layer at all.
func EnvelopeHandler(builder *benzene.ApplicationBuilder) HandlerFunc {
	return func(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
		var req wire.Request
		if err := json.Unmarshal(event, &req); err != nil {
			return nil, fmt.Errorf("awslambda: malformed envelope: %w", err)
		}

		resp := envelope.Dispatch(ctx, builder.Pipeline, builder.Container, req)
		return json.Marshal(resp)
	}
}
