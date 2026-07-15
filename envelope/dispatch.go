// Package envelope dispatches a wire.Request through a benzene.Pipeline and produces a
// wire.Response - the shared glue transport-bindings.md calls "the raw BenzeneMessage
// envelope for direct invocation": used directly by any binding with no richer native
// contract (queues without attribute support, direct function invocation), and reused here
// by the conformance runner (which only needs to prove pipeline/status-mapping behavior, not
// a real network round-trip) and by httpbinding's EnvelopeHandler (which exposes it over
// HTTP for cross-service interop).
package envelope

import (
	"context"
	"encoding/json"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/wire"
)

// Dispatch resolves req.Topic against pipeline (via container.NewScope() for this
// invocation's DI scope, per core-concepts.md §8: one scope per invocation) and returns the
// wire-contracts.md §1.2 response envelope. It never returns a Go error - every outcome,
// including "no handler" and a handler panic, is represented as a wire.Response, since a
// binding built on this always needs to produce SOME response for its caller.
func Dispatch(ctx context.Context, pipeline *benzene.Pipeline, container *benzene.Container, req wire.Request) wire.Response {
	scope := container.NewScope()
	ic := benzene.NewInvocationContext(benzene.NewTopic(req.Topic), req.Headers, json.RawMessage(req.Body), scope)

	if err := pipeline.Run(ctx, ic); err != nil {
		return errorResponse(benzene.ServiceUnavailable[any](err.Error()))
	}
	if ic.Result == nil {
		return errorResponse(benzene.UnexpectedError[any]("pipeline completed without producing a result"))
	}
	return toResponse(ic.Result)
}

func toResponse(result benzene.ResultInfo) wire.Response {
	status := result.ResultStatus()
	if !status.IsSuccess() {
		return errorResponse(result)
	}

	payload := result.ResultPayload()
	if payload == nil {
		return wire.Response{StatusCode: string(status), Headers: map[string]string{}, Body: ""}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return errorResponse(benzene.UnexpectedError[any]("failed to serialize response payload: " + err.Error()))
	}
	return wire.Response{
		StatusCode: string(status),
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(body),
	}
}

func errorResponse(result benzene.ResultInfo) wire.Response {
	payload := wire.ErrorPayload{
		Status: string(result.ResultStatus()),
		Detail: strings.Join(result.ResultErrors(), ", "),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		// ErrorPayload is a plain struct of strings - Marshal cannot fail on it in practice,
		// but degrade to an empty body rather than panic if it somehow ever does.
		body = []byte("{}")
	}
	return wire.Response{
		StatusCode: string(result.ResultStatus()),
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(body),
	}
}
