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
	resp, _ := DispatchResult(ctx, pipeline, container, req)
	return resp
}

// DispatchResult is Dispatch plus the invocation's in-process success flag (the result's
// IsSuccessful). The wire.Response is identical and unchanged - the flag is a separate return,
// never part of the serialized envelope (the wire contract carries only the status). It exists
// for in-process bindings that must decide acknowledge-vs-retry the way the .NET applications do:
// on the result's success flag, not the wire status class. The two coincide for framework
// statuses but differ for an application-defined failure status (Fail, unsuccessful) and the
// health-check "service-unavailable but successful" shape (SetResult), where classifying by the
// status alone would nack a message the handler acked, or ack one it failed.
func DispatchResult(ctx context.Context, pipeline *benzene.Pipeline, container *benzene.Container, req wire.Request) (wire.Response, bool) {
	return DispatchTopicResult(ctx, pipeline, container, benzene.NewTopic(req.Topic), req.Headers, req.Body)
}

// DispatchTopicResult dispatches an explicit, already-resolved topic - including its version -
// through the pipeline. DispatchResult passes an id-only topic and lets RouterMiddleware read
// the version off the message headers (wire-contracts.md §2 tier C); DispatchTopicResult instead
// names the version in application code, which the router then leaves untouched (a version
// already on the topic wins over the header). It exists for fan-in / streaming-shaped bindings
// (core-concepts.md §3, e.g. the Cosmos DB Change Feed trigger) that name their target topic in
// application code rather than parsing it out of message metadata, so a developer can fan a feed
// into a versioned handler by explicit programmatic dispatch. The wire.Response and success flag
// are produced identically to DispatchResult.
func DispatchTopicResult(ctx context.Context, pipeline *benzene.Pipeline, container *benzene.Container, topic benzene.Topic, headers map[string]string, body string) (wire.Response, bool) {
	scope := container.NewScope()
	ic := benzene.NewInvocationContext(topic, headers, json.RawMessage(body), scope)

	if err := pipeline.Run(ctx, ic); err != nil {
		return withResponseHeaders(errorResponse(benzene.ServiceUnavailable[any](err.Error())), ic), false
	}
	if ic.Result == nil {
		return withResponseHeaders(errorResponse(benzene.UnexpectedError[any]("pipeline completed without producing a result")), ic), false
	}
	return withResponseHeaders(toResponse(ic.Result), ic), resultSuccessful(ic.Result)
}

// resultSuccessful reads the result's success flag off the type-erased ResultInfo (via the
// optional ResultIsSuccessful interface every Result[T] satisfies), falling back to the status
// class if some other ResultInfo implementation does not expose it.
func resultSuccessful(result benzene.ResultInfo) bool {
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		return s.ResultIsSuccessful()
	}
	return !result.ResultStatus().IsFailure()
}

// withResponseHeaders merges headers set during the invocation (by middleware, or by a
// handler via benzene.SetResponseHeader) onto the response envelope - after the defaults, so
// an invocation-set header wins over the default content-type, matching the "opinionated but
// optional" steer everywhere else.
func withResponseHeaders(resp wire.Response, ic *benzene.InvocationContext) wire.Response {
	if len(ic.ResponseHeaders) == 0 {
		return resp
	}
	if resp.Headers == nil {
		resp.Headers = make(map[string]string, len(ic.ResponseHeaders))
	}
	for name, value := range ic.ResponseHeaders {
		resp.Headers[name] = value
	}
	return resp
}

func toResponse(result benzene.ResultInfo) wire.Response {
	status := result.ResultStatus()
	// A framework failure status renders as an error payload; a success status and an
	// application-defined (unknown) status both carry their payload, matching .NET's
	// IsSuccessful (= !IsFailure) so custom statuses flow through untouched. A result may also
	// set its success flag explicitly (SetResult) - e.g. the health check returning
	// service-unavailable but rendering its report body - which this honours over the status.
	successful := !status.IsFailure()
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		successful = s.ResultIsSuccessful()
	}
	if !successful {
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
