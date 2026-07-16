// Package benzenetest is an in-process test host for applications built on benzene-go - the Go
// counterpart to the main daniellepelley/Benzene repo's Benzene.Testing / BenzeneTestHost. It
// is for a *consuming* application's own tests (exercising its registered handlers and pipeline
// wiring, middleware included, without spinning up real HTTP/Lambda/Azure Functions/etc.), not
// for this repo's own test suite, which already builds an InvocationContext directly wherever
// that's the clearer choice.
package benzenetest

import (
	"context"

	benzene "github.com/daniellepelley/benzene-go"
)

// Invoke runs one pipeline invocation in-process against builder - the direct equivalent of a
// real transport delivering an event - and returns the resulting Result[TRes], with the
// underlying handler's payload type-asserted into TRes.
//
// request is passed straight through as the raw request value (the registry's zero-copy path,
// per registry.go's convertRequest: "if raw already *is* TReq, pass it through untouched"), so
// no serialization round-trip is needed - construct request exactly as the target handler's own
// TReq. headers may be nil.
//
// If the pipeline itself errors, or produces no result at all, Invoke returns
// ServiceUnavailable / UnexpectedError respectively - mirroring envelope.Dispatch's own
// "every outcome becomes a Result, never a raw error" rule, so a test always gets a Result back
// to assert on rather than needing to separately handle a Go error return.
func Invoke[TReq, TRes any](ctx context.Context, builder *benzene.ApplicationBuilder, topic benzene.Topic, headers map[string]string, request TReq) benzene.Result[TRes] {
	scope := builder.Container.NewScope()
	ic := benzene.NewInvocationContext(topic, headers, request, scope)

	if err := builder.Pipeline.Run(ctx, ic); err != nil {
		return benzene.ServiceUnavailable[TRes](err.Error())
	}
	if ic.Result == nil {
		return benzene.UnexpectedError[TRes]("pipeline completed without producing a result")
	}
	return convertResult[TRes](ic.Result)
}

// convertResult type-asserts result's payload into TRes. A payload that isn't a TRes (a
// mismatch between the TRes the caller asked for and what the dispatched handler actually
// returned - a test-authoring error, not a runtime condition this library can prevent at
// compile time) is left as a nil Payload rather than panicking, so a test sees a clear
// zero-value payload to fail its own assertion on instead of a crash.
func convertResult[TRes any](result benzene.ResultInfo) benzene.Result[TRes] {
	out := benzene.Result[TRes]{Status: result.ResultStatus(), Errors: result.ResultErrors()}
	if payload := result.ResultPayload(); payload != nil {
		if typed, ok := payload.(TRes); ok {
			out.Payload = &typed
		}
	}
	return out
}
