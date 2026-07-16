// Package awslambda deploys a Benzene application to AWS Lambda. It hand-implements the
// Lambda Runtime API (https://docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html) rather
// than depending on the official github.com/aws/aws-lambda-go module, to keep this repo's
// zero-third-party-dependency policy - the protocol is small, stable, and has been unchanged
// since custom runtimes launched.
//
// Build the binary as `bootstrap`, GOOS=linux, and deploy it with the `provided.al2023` (or
// `provided.al2`) runtime - see examples/aws-lambda-helloworld for a complete deployable
// setup (Dockerfile + SAM template).
package awslambda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

// HandlerFunc is what the bootstrap loop calls once per invocation: raw event JSON in, raw
// response JSON out. This is deliberately decoupled from any particular event shape (API
// Gateway, a Function URL, or a direct envelope invoke) so HTTPHandler and EnvelopeHandler can
// each adapt independently and be unit-tested without any network calls - see http.go and
// envelope.go.
type HandlerFunc func(ctx context.Context, event json.RawMessage) (json.RawMessage, error)

// Start runs the Lambda Runtime API bootstrap loop: poll for the next invocation, call
// handler, POST the result or error back, forever. It reads AWS_LAMBDA_RUNTIME_API from the
// environment, which the Lambda execution environment sets automatically - Start is only
// meant to be called from within a deployed Lambda function, never in a test (use runOnce
// directly against a fake runtime API server for that; see runtime_test.go).
//
// Start never returns under normal operation - only on a fatal runtime-API communication
// failure, matching the real Lambda runtime's own behavior of restarting the whole execution
// environment when that happens.
func Start(handler HandlerFunc) {
	client, err := newRuntimeClientFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	for {
		if err := runOnce(ctx, client, handler); err != nil {
			log.Fatalf("awslambda: %v", err)
		}
	}
}

func newRuntimeClientFromEnv() (*runtimeClient, error) {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	if api == "" {
		return nil, errors.New("awslambda: AWS_LAMBDA_RUNTIME_API is not set - Start must run inside the Lambda execution environment")
	}
	return &runtimeClient{baseURL: "http://" + api + "/2018-06-01/runtime", httpClient: http.DefaultClient}, nil
}

// runOnce fetches exactly one invocation, calls handler (with panic recovery - a handler
// panic must not crash the whole runtime process, matching core-concepts.md §5's "a handler
// panic MUST NOT crash the transport adapter" rule), and posts the outcome back. The returned
// error is a runtime-API communication failure (network error, unexpected status); a handler
// error or panic is reported to the Runtime API's own error endpoint and does NOT surface as
// a returned error, since that's a normal, expected outcome for one invocation, not a reason
// to stop the whole loop.
func runOnce(ctx context.Context, client *runtimeClient, handler HandlerFunc) error {
	inv, err := client.next()
	if err != nil {
		return fmt.Errorf("fetching next invocation: %w", err)
	}

	if inv.TraceID != "" {
		os.Setenv("_X_AMZN_TRACE_ID", inv.TraceID)
	}

	invokeCtx := ctx
	if inv.DeadlineMs > 0 {
		var cancel context.CancelFunc
		invokeCtx, cancel = context.WithDeadline(ctx, time.UnixMilli(inv.DeadlineMs))
		defer cancel()
	}

	result, invokeErr := invoke(invokeCtx, handler, inv.Payload)
	if invokeErr != nil {
		if err := client.postError(inv.RequestID, invokeErr); err != nil {
			return fmt.Errorf("posting invocation error: %w", err)
		}
		return nil
	}

	if err := client.postResponse(inv.RequestID, result); err != nil {
		return fmt.Errorf("posting invocation response: %w", err)
	}
	return nil
}

func invoke(ctx context.Context, handler HandlerFunc, event json.RawMessage) (result json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panicked: %v", r)
		}
	}()
	return handler(ctx, event)
}

type invocation struct {
	RequestID  string
	DeadlineMs int64
	TraceID    string
	Payload    json.RawMessage
}

type runtimeClient struct {
	baseURL    string
	httpClient *http.Client
}

func (c *runtimeClient) next() (invocation, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/invocation/next")
	if err != nil {
		return invocation{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return invocation{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return invocation{}, fmt.Errorf("unexpected status %d fetching next invocation: %s", resp.StatusCode, body)
	}

	deadlineMs, _ := strconv.ParseInt(resp.Header.Get("Lambda-Runtime-Deadline-Ms"), 10, 64)
	return invocation{
		RequestID:  resp.Header.Get("Lambda-Runtime-Aws-Request-Id"),
		DeadlineMs: deadlineMs,
		TraceID:    resp.Header.Get("Lambda-Runtime-Trace-Id"),
		Payload:    json.RawMessage(body),
	}, nil
}

func (c *runtimeClient) postResponse(requestID string, payload json.RawMessage) error {
	resp, err := c.httpClient.Post(c.baseURL+"/invocation/"+requestID+"/response", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status %d posting invocation response", resp.StatusCode)
	}
	return nil
}

// runtimeError is the Lambda Runtime API's error payload shape.
type runtimeError struct {
	ErrorMessage string `json:"errorMessage"`
	ErrorType    string `json:"errorType"`
}

func (c *runtimeClient) postError(requestID string, invokeErr error) error {
	body, err := json.Marshal(runtimeError{ErrorMessage: invokeErr.Error(), ErrorType: "HandlerError"})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/invocation/"+requestID+"/error", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Lambda-Runtime-Function-Error-Type", "Unhandled")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status %d posting invocation error", resp.StatusCode)
	}
	return nil
}
