// Package awslambdaclient is the outbound Lambda-invoking client of daniellepelley/Benzene's
// docs/specification/transport-bindings.md §2 ("Outbound clients"): one interface -
// Send(topic, headers, message) -> Result - that invokes a target AWS Lambda function with a
// wire-contracts.md envelope as its payload. It is the Go port of
// Benzene.Clients.Aws.Lambda (AwsLambdaBenzeneMessageClient), and the invoking counterpart of
// the inbound awslambda binding: this side calls a Lambda, that side is called by one.
package awslambdaclient

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/wire"
)

// InvokeAPI is the single Lambda SDK method Client depends on. Depending on this narrow
// interface, rather than the concrete *lambda.Client, makes Client testable with a fake - no real
// AWS calls (and no SigV4 mocking) needed in tests. *lambda.Client satisfies it as-is.
type InvokeAPI interface {
	Invoke(ctx context.Context, params *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// Client invokes a target Lambda function with an outbound Benzene message. It satisfies
// client.Sender, so it can be wrapped in client.WithCorrelationID/WithRetry like any
// other Sender.
type Client struct {
	API          InvokeAPI
	FunctionName string

	// InvocationType selects request/response (types.InvocationTypeRequestResponse, the
	// NewClient default - the target's envelope response is parsed back into the Result) or
	// fire-and-forget (types.InvocationTypeEvent - Send returns StatusAccepted without waiting
	// for or parsing a response body). types.InvocationTypeDryRun is accepted by the SDK but has
	// no envelope response either, so it is treated like Event here.
	InvocationType types.InvocationType
}

// NewClient returns a Client invoking functionName via api (typically a *lambda.Client
// constructed from your own AWS config). It defaults InvocationType to RequestResponse; set the
// InvocationType field to types.InvocationTypeEvent for fire-and-forget invocation.
func NewClient(api InvokeAPI, functionName string) *Client {
	return &Client{API: api, FunctionName: functionName, InvocationType: types.InvocationTypeRequestResponse}
}

// Send invokes the target function with a wire-contracts.md envelope (topic/headers/message) as
// its payload and maps the outcome back to a Result[json.RawMessage].
//
// A transport-level failure (the invoke call itself erroring) maps to ServiceUnavailable, per
// transport-bindings §1.7's failure rule: the caller always gets a Result back, never a Go error.
// For a fire-and-forget (Event) invocation there is no response body, so a successful invoke maps
// to StatusAccepted. For a request/response invocation, a set FunctionError means the target
// function threw - AWS returns the error object, not the normal output, as the payload, so that is
// surfaced as UnexpectedError rather than mis-parsed as a success. Otherwise the payload is parsed
// as the target's wire envelope response and converted with the same rules as httpclient. Use
// Unmarshal to convert the raw payload into an application type.
func (c *Client) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	payload, err := wire.MarshalRequest(wire.Request{Topic: topic.String(), Headers: headers, Body: string(message)})
	if err != nil {
		// wire.Request is a plain struct of strings and a string map - Marshal cannot fail on
		// it in practice, but degrade gracefully rather than panic if it somehow ever does.
		return benzene.ServiceUnavailable[json.RawMessage]("awslambdaclient: failed to serialize outbound request: " + err.Error())
	}

	out, err := c.API.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(c.FunctionName),
		InvocationType: c.InvocationType,
		Payload:        payload,
	})
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("awslambdaclient: invoke failed: " + err.Error())
	}

	// A fire-and-forget invocation returns no response payload to parse.
	if c.InvocationType != types.InvocationTypeRequestResponse {
		return benzene.Result[json.RawMessage]{Status: benzene.StatusAccepted}
	}

	// A RequestResponse invoke where the function threw returns with FunctionError set and an
	// error object (not the normal output) as the payload. Surface that as a failure rather than
	// mis-parsing the error object as the target's response envelope.
	if out.FunctionError != nil && *out.FunctionError != "" {
		return benzene.UnexpectedError[json.RawMessage]("awslambdaclient: function " + c.FunctionName + " reported a " + *out.FunctionError + " error: " + string(out.Payload))
	}

	resp, err := wire.UnmarshalResponse(out.Payload)
	if err != nil {
		return benzene.ServiceUnavailable[json.RawMessage]("awslambdaclient: failed to parse invoke envelope response: " + err.Error())
	}
	return toResult(resp)
}

// toResult converts the target's wire envelope response into a Result[json.RawMessage], mirroring
// httpclient.toResult: a framework failure status is read back as an error payload; a success or
// application-defined (unknown) status carries its body as the payload.
func toResult(resp wire.Response) benzene.Result[json.RawMessage] {
	status := benzene.Status(resp.StatusCode)
	if status.IsFailure() {
		errPayload, parseErr := wire.UnmarshalErrorPayload([]byte(resp.Body))
		// Messages() prefers the problem document's errors array, which is authoritative and
		// ordered, and falls back to detail as ONE opaque message - never splitting it on ", ",
		// a rule the RFC 9457 revision withdrew because error messages contain commas.
		if parseErr != nil {
			return benzene.Fail[json.RawMessage](status)
		}
		return benzene.Fail[json.RawMessage](status, errPayload.Messages()...)
	}

	if resp.Body == "" {
		return benzene.Result[json.RawMessage]{Status: status}
	}
	payload := json.RawMessage(resp.Body)
	return benzene.Result[json.RawMessage]{Status: status, Payload: &payload}
}

var _ client.Sender = (*Client)(nil)
