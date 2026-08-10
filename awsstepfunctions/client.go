// Package awsstepfunctions is the outbound Step Functions client of daniellepelley/Benzene's
// docs/specification/transport-bindings.md §2 ("Outbound clients"): one interface -
// Send(topic, headers, message) -> Result - that starts an AWS Step Functions state-machine
// execution with a wire-contracts.md envelope as its input. It is the Go port of
// Benzene.Clients.Aws.StepFunctions (StepFunctionsClient). Starting an execution is inherently
// asynchronous, so a successful start maps to StatusAccepted, like every other fire-and-forget
// Sender in this repo.
package awsstepfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sfn/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// StartExecutionAPI is the single Step Functions SDK method Client depends on. Depending on this
// narrow interface, rather than the concrete *sfn.Client, makes Client testable with a fake - no
// real AWS calls (and no SigV4 mocking) needed in tests. *sfn.Client satisfies it as-is.
type StartExecutionAPI interface {
	StartExecution(ctx context.Context, params *sfn.StartExecutionInput, optFns ...func(*sfn.Options)) (*sfn.StartExecutionOutput, error)
}

// Client starts executions of a Step Functions state machine with outbound Benzene messages. It
// satisfies client.Sender, so it can be wrapped in client.WithCorrelationID/WithRetry like
// any other Sender.
type Client struct {
	API             StartExecutionAPI
	StateMachineArn string

	// ExecutionName, when non-nil, derives the execution name for a Send call. A supplied name
	// makes the start idempotent for the same (state machine, name, input) - a redelivery reusing
	// the same name is a no-op start rather than a duplicate execution. The returned name is
	// sanitized to the Step Functions rules (see sanitizeExecutionName). Leave nil to let AWS
	// generate a UUID name (a distinct execution per call).
	ExecutionName func(topic benzene.Topic, message []byte) string
}

// NewClient returns a Client starting executions of stateMachineArn via api (typically an
// *sfn.Client constructed from your own AWS config).
func NewClient(api StartExecutionAPI, stateMachineArn string) *Client {
	return &Client{API: api, StateMachineArn: stateMachineArn}
}

// Send starts a new state-machine execution with a wire-contracts.md envelope (topic/headers/
// message) as its Input. Starting an execution is fire-and-forget - there is no synchronous
// response body - so a successful start maps to StatusAccepted, matching wire-contracts.md §3
// ("Accepted for asynchronous processing"). A transport-level failure maps to ServiceUnavailable,
// per transport-bindings §1.7's failure rule: the caller always gets a Result back, never a Go
// error.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	input, err := wire.MarshalRequest(wire.Request{Topic: topic.String(), Headers: headers, Body: string(message)})
	if err != nil {
		// wire.Request is a plain struct of strings and a string map - Marshal cannot fail on
		// it in practice, but degrade gracefully rather than panic if it somehow ever does.
		return benzene.ServiceUnavailable[json.RawMessage]("awsstepfunctions: failed to serialize outbound request: " + err.Error())
	}

	params := &sfn.StartExecutionInput{
		StateMachineArn: aws.String(c.StateMachineArn),
		Input:           aws.String(string(input)),
	}
	if c.ExecutionName != nil {
		params.Name = aws.String(sanitizeExecutionName(c.ExecutionName(topic, message)))
	}

	if _, err := c.API.StartExecution(ctx, params); err != nil {
		// An idempotent retry: starting with an execution name that already exists (same name +
		// input) is a no-op start, not a failure - matching .NET's catch of ExecutionAlreadyExists.
		// Only when ExecutionName is set can this occur (an auto-generated UUID name never collides).
		var already *types.ExecutionAlreadyExists
		if errors.As(err, &already) {
			return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
		}
		return benzene.ServiceUnavailable[json.RawMessage]("awsstepfunctions: start execution failed: " + err.Error())
	}
	return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
}

// disallowedExecutionNameChars is the set Step Functions rejects in an execution name, alongside
// whitespace and control characters (mirroring Benzene.Clients.Aws.StepFunctions'
// SanitizeExecutionName).
const disallowedExecutionNameChars = "<>{}[]?*\"#%\\^|~`$&,;:/"

// sanitizeExecutionName turns an idempotency token into a valid Step Functions execution name:
// Step Functions rejects whitespace, control characters, and disallowedExecutionNameChars, and
// caps the name at 80 characters. Disallowed runes are replaced with '-'. An empty token is
// returned unchanged (an empty Name is functionally the same as omitting it - AWS then generates
// a UUID).
func sanitizeExecutionName(name string) string {
	if name == "" {
		return ""
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(disallowedExecutionNameChars, r) {
			out = append(out, '-')
			continue
		}
		out = append(out, r)
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return string(out)
}

var _ client.Sender = (*Client)(nil)
