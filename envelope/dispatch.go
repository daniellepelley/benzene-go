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
	"fmt"

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

// DispatchResult is Dispatch plus the invocation's success flag (the result's IsSuccessful, the
// envelope's isSuccessful). It exists for bindings that decide acknowledge-vs-retry: they settle on
// the result's own flag, never on the status text (wire-contracts.md §1.2), which for an
// application-defined status is the only signal there is. The flag is false for a failure result,
// for no result at all, and for an error the pipeline returned.
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
	resp, successful, _ := dispatch(ctx, pipeline, container, topic, headers, body)
	return resp, successful
}

// dispatch runs one invocation and reports its response envelope, its success flag, and whether
// it ended as an unmatched topic (benzene.IsUnmatchedTopic) - the three facts a one-way binding
// settles on.
func dispatch(ctx context.Context, pipeline *benzene.Pipeline, container *benzene.Container, topic benzene.Topic, headers map[string]string, body string) (resp wire.Response, successful, unmatched bool) {
	scope := container.NewScope()
	ic := benzene.NewInvocationContext(topic, headers, json.RawMessage(body), scope)

	if err := pipeline.Run(ctx, ic); err != nil {
		return withResponseHeaders(errorResponse(benzene.ServiceUnavailable[any](err.Error())), ic), false, false
	}
	if ic.Result == nil {
		return withResponseHeaders(errorResponse(benzene.UnexpectedError[any]("pipeline completed without producing a result")), ic), false, false
	}
	return withResponseHeaders(toResponse(ic.Result), ic), resultSuccessful(ic.Result), benzene.IsUnmatchedTopic(ic.Result)
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
	// The branch is on the result's isSuccessful (wire-contracts.md §1.3), never the status text:
	// a framework failure status and a failed application-defined status (Fail) render a problem
	// document; a success status and a plain application-defined status carry their payload.
	successful := !status.IsFailure()
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		successful = s.ResultIsSuccessful()
	}
	if !successful {
		return errorResponse(result)
	}

	payload := result.ResultPayload()
	if payload == nil {
		return wire.Response{
			StatusCode:   string(status),
			Headers:      map[string]string{},
			Body:         "",
			IsSuccessful: boolPtr(true),
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return errorResponse(benzene.UnexpectedError[any]("failed to serialize response payload: " + err.Error()))
	}
	return wire.Response{
		StatusCode: string(status),
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(body),
		// Stated outright (§1.2) rather than left for a peer to infer from the status: for an
		// application-defined status it is the only signal of success a receiver has.
		IsSuccessful: boolPtr(true),
	}
}

func boolPtr(v bool) *bool { return &v }

// problemDocument builds the problem document for a failed result (wire-contracts.md §1.3).
//
// An application-authored document (benzene.ProblemResult) is emitted verbatim - deriving one from
// the status instead would overwrite the application's own `type` URI with the registry URI, which
// is the whole point of authoring it. Otherwise the document is derived from the status and the
// result's structured errors, so a field and a code survive to the caller rather than being
// flattened into prose.
func problemDocument(result benzene.ResultInfo) wire.ErrorPayload {
	if authored, ok := result.(benzene.ProblemDocumentInfo); ok {
		if document := authored.ResultProblemDocument(); document != nil {
			return *document
		}
	}
	return wire.NewProblem(string(result.ResultStatus()), benzene.ProblemsOf(result))
}

func errorResponse(result benzene.ResultInfo) wire.Response {
	payload := problemDocument(result)
	body, err := json.Marshal(payload)
	if err != nil {
		// ErrorPayload is a plain struct of strings and slices - Marshal cannot fail on it in
		// practice, but degrade to an empty body rather than panic if it somehow ever does.
		body = []byte("{}")
	}
	return wire.Response{
		StatusCode: string(result.ResultStatus()),
		// application/problem+json: the body IS an RFC 9457 problem document (§1.3). Readers must
		// accept application/json too, so this is safe for an existing peer.
		Headers:      map[string]string{"content-type": "application/problem+json"},
		Body:         string(body),
		IsSuccessful: boolPtr(false),
	}
}

// ToResult rebuilds a Result from a response envelope a peer sent back - the receiving half of
// wire-contracts.md §1.2, shared by every outbound client that speaks the envelope (httpclient,
// awslambdaclient). The envelope's isSuccessful decides, never the statusCode text: a failure is
// read back from its problem document (§1.3) with each error's field and code intact, and a success
// carries its body as the raw JSON payload. That is what lets an application-defined status from a
// peer arrive with the meaning the peer gave it.
//
// An envelope that omits isSuccessful is malformed and is read as unsuccessful (§1.2) - never
// classified from its statusCode. One whose isSuccessful contradicts a vocabulary statusCode (a
// "not-found" marked successful, or an "ok" marked or left unsuccessful) is read as an
// unexpected-error naming the contradiction, rather than as either half of it.
func ToResult(resp wire.Response) benzene.Result[json.RawMessage] {
	status := benzene.Status(resp.StatusCode)
	successful := resp.IsSuccessful != nil && *resp.IsSuccessful

	if (successful && status.IsFailure()) || (!successful && status.IsSuccess()) {
		return benzene.UnexpectedError[json.RawMessage](fmt.Sprintf(
			"malformed response envelope: statusCode %q contradicts isSuccessful %v", status, successful))
	}
	if !successful {
		problem, err := wire.UnmarshalErrorPayload([]byte(resp.Body))
		if err != nil {
			return benzene.Fail[json.RawMessage](status)
		}
		// Problems(), not Messages(): a peer that sent a field and a code went to the trouble of
		// knowing them, and a caller that re-renders or re-raises this failure should still have
		// them. It prefers the errors array (authoritative, ordered) and falls back to detail as
		// ONE opaque message - never splitting it on ", ", since messages contain commas.
		return benzene.FailWith[json.RawMessage](status, problem.Problems()...)
	}

	if resp.Body == "" {
		return benzene.Result[json.RawMessage]{Status: status}
	}
	payload := json.RawMessage(resp.Body)
	return benzene.Result[json.RawMessage]{Status: status, Payload: &payload}
}
