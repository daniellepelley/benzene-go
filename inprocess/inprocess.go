// Package inprocess dispatches an outbound send straight to a handler pipeline built in the
// same runtime, in the shared []byte/json.RawMessage envelope every client.Sender uses, without
// going over any wire (no SQS/SNS/HTTP/socket - not even loopback). It exists for the case where
// functionality that used to live in a different service has been moved into the caller's own
// service, and the topic that used to be sent over a real transport now has no reason to leave
// the process.
//
// See the cross-language modular monolith pattern for the shape this is written toward:
// https://github.com/daniellepelley/Benzene/blob/main/docs/patterns/modular-monolith.md
//
// # PORT DIVERGENCE from .NET/TypeScript
//
// Those ports build one named in-process pipeline per module inside a *single* registration
// call and route to it from a shared, container-wide outbound routing table
// (OutboundRoutingBuilder / addOutboundRouting) via a name-only useInProcess(name). This Go
// port has neither of those things to slot into, for reasons specific to this port's own
// architecture, not a limitation introduced here:
//
//   - There is no outbound routing table in benzene-go at all - client.Sender is a single
//     interface an application constructs and uses directly (client.RegisterSender), the same
//     way awssqs.Client or httpclient.Client would be. An in-process Sender fits the exact same
//     shape: construct it, use it directly, no route table to hook into.
//   - benzene.Registry is per-instance, not a container-wide/process-wide singleton the way
//     .NET's MessageHandlerDefinitionIndex or TypeScript's MessageHandlersRegistry-derived
//     finder are. Each named pipeline in a PipelineSet has its own independent
//     *benzene.ApplicationBuilder (its own Registry, Container, Pipeline), so two different
//     named pipelines CAN legitimately register a handler for the literal same topic with zero
//     collision - unlike .NET/TypeScript, FanOutSender needs no per-target topic workaround
//     (see DuplicateInProcessFanOutTargetException in those ports for why they needed one).
//   - PipelineSet.Add returns an error for a duplicate name, and NewSender/NewFanOutSender
//     return an error for a name that isn't registered - checked at construction time, which in
//     Go's explicit-wiring style already happens once, at startup, in Configure. There is no
//     separate boot-time-validation mechanism to add on top (.NET's IStartUpCheck has no
//     analogue in this port), because there is nothing lazy to validate: a Sender that failed to
//     construct simply doesn't exist to be used.
package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	benzene "github.com/daniellepelley/benzene-go"
)

// PipelineSet accumulates one named in-process pipeline per module - the Go counterpart of
// .NET's InProcessMessagingBuilder / TypeScript's InProcessMessagingBuilder, adapted to this
// port's explicit-construction idiom: there is no top-level "one registration call" to guard
// (see the package doc comment), so PipelineSet is a plain accumulator an application builds
// once at startup and adds named *benzene.ApplicationBuilder values to.
type PipelineSet struct {
	builders map[string]*benzene.ApplicationBuilder
}

// NewPipelineSet returns an empty PipelineSet.
func NewPipelineSet() *PipelineSet {
	return &PipelineSet{builders: make(map[string]*benzene.ApplicationBuilder)}
}

// Add registers builder under name - typically the result of running one module's own
// benzene.App[TConfig].Run() (its own Registry, Container, and Pipeline, independent of every
// other named pipeline in this set). Returns an error if name is already registered, or if
// builder has no Pipeline set (UsePipeline was never called) - a builder with no pipeline can
// never dispatch anything, so failing at Add time names the mistake immediately rather than
// producing a confusing nil-pointer failure on first send.
func (s *PipelineSet) Add(name string, builder *benzene.ApplicationBuilder) error {
	if _, exists := s.builders[name]; exists {
		return fmt.Errorf("inprocess: pipeline %q was already added to this PipelineSet", name)
	}
	if builder.Pipeline == nil {
		return fmt.Errorf("inprocess: pipeline %q has no Pipeline set (call ApplicationBuilder.UsePipeline before Add)", name)
	}
	s.builders[name] = builder
	return nil
}

// Names returns every registered pipeline name, for diagnostics (error messages).
func (s *PipelineSet) Names() []string {
	names := make([]string, 0, len(s.builders))
	for name := range s.builders {
		names = append(names, name)
	}
	return names
}

// Sender is a client.Sender (see the client package) that dispatches straight to one named
// pipeline in a PipelineSet, without leaving the process.
type Sender struct {
	builder *benzene.ApplicationBuilder
	name    string
}

// NewSender returns a Sender bound to the pipeline registered under name in set.
//
// Returns an error if name is not registered - checked here, at construction, rather than
// deferred to first send: in this port's explicit-wiring style a Sender is built once in
// Configure and then used, so a construction-time error is the natural place to catch a typo'd
// or forgotten name, with no separate boot-time-check mechanism needed on top (see the package
// doc comment).
func NewSender(set *PipelineSet, name string) (*Sender, error) {
	builder, ok := set.builders[name]
	if !ok {
		return nil, fmt.Errorf("inprocess: no pipeline named %q is registered in this PipelineSet (registered: %v)", name, set.Names())
	}
	return &Sender{builder: builder, name: name}, nil
}

// Send dispatches message to the bound pipeline in a fresh invocation scope (the same
// isolation a message sent over a real transport would get in the receiving process, not the
// sending call's scope) and returns its result, marshaled into Result[json.RawMessage] exactly
// as every other client.Sender does (see httpclient.Client.Send) - the request body is still
// serialized to JSON on the way in and the response payload still marshaled to JSON on the way
// out; this transport removes the network hop, not the serialize/deserialize step, so a handler
// behind it sees exactly the same shape of request a real transport would hand it.
func (s *Sender) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	return dispatch(ctx, s.builder, topic, headers, message)
}

// FanOutSender is a client.Sender that dispatches one send to several named pipelines in a
// PipelineSet concurrently - the in-monolith equivalent of one SNS topic fanning out to several
// subscribers. Unlike the .NET and TypeScript ports' fan-out, no per-target topic is needed:
// each named pipeline has its own independent benzene.Registry (not shared process-wide), so
// two targets CAN legitimately both handle the literal same topic with zero collision - see the
// package doc comment.
type FanOutSender struct {
	builders []*benzene.ApplicationBuilder
	names    []string
	logger   *slog.Logger
}

// NewFanOutSender returns a FanOutSender bound to every name in names, all in set.
//
// Returns an error if names is empty, or if any name is not registered in set - checked here,
// at construction, for the same reason NewSender checks eagerly (see its doc comment).
// logger receives a Warn-level line naming the pipeline and topic for each target that fails
// (a thrown/returned Go error) or returns an unsuccessful status - there is no in-process DLQ,
// so this is the only place such a failure is visible. A nil logger uses slog.Default(),
// matching this port's logging.Middleware convention.
func NewFanOutSender(set *PipelineSet, logger *slog.Logger, names ...string) (*FanOutSender, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("inprocess: NewFanOutSender requires at least one pipeline name")
	}
	builders := make([]*benzene.ApplicationBuilder, len(names))
	for i, name := range names {
		builder, ok := set.builders[name]
		if !ok {
			return nil, fmt.Errorf("inprocess: no pipeline named %q is registered in this PipelineSet (registered: %v)", name, set.Names())
		}
		builders[i] = builder
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &FanOutSender{builders: builders, names: names, logger: logger}, nil
}

// Send dispatches message to every bound pipeline concurrently, each in its own fresh
// invocation scope, and returns an unconditional success once accepted - matching what a real
// SNS publish returns (no visibility into subscriber outcomes). Each target's failure is
// isolated: logged, but does not affect the other targets or this method's own (always
// successful) return value. There is no in-process DLQ - a target's failure is genuinely lost
// unless its own handler retries internally.
func (s *FanOutSender) Send(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	var wg sync.WaitGroup
	for i, builder := range s.builders {
		wg.Add(1)
		go func(name string, builder *benzene.ApplicationBuilder) {
			defer wg.Done()
			result := dispatch(ctx, builder, topic, headers, message)
			if !result.IsSuccessful() {
				s.logger.Warn("in-process fan-out consumer returned an unsuccessful result",
					"pipeline", name, "topic", topic.String(), "status", string(result.Status))
			}
		}(s.names[i], builder)
	}
	wg.Wait()
	return benzene.Ok[json.RawMessage](json.RawMessage("null"))
}

// dispatch runs one pipeline invocation against builder in a fresh scope and converts its
// ResultInfo into Result[json.RawMessage] - the shared mechanics behind both Sender.Send and
// FanOutSender.Send. Mirrors benzenetest.Invoke's scope/InvocationContext/Pipeline.Run
// sequence, generalized to the type-erased client.Sender contract (benzenetest.Invoke is
// typed, TReq/TRes generic, and test-only).
func dispatch(ctx context.Context, builder *benzene.ApplicationBuilder, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
	scope := builder.Container.NewScope()
	ic := benzene.NewInvocationContext(topic, headers, message, scope)

	if err := builder.Pipeline.Run(ctx, ic); err != nil {
		return benzene.ServiceUnavailable[json.RawMessage](err.Error())
	}
	if ic.Result == nil {
		return benzene.UnexpectedError[json.RawMessage]("pipeline completed without producing a result")
	}
	return toRawResult(ic.Result)
}

// toRawResult marshals a ResultInfo's payload into Result[json.RawMessage], matching every
// other outbound client's Send contract (see httpclient.Client.Send's own toResult).
func toRawResult(result benzene.ResultInfo) benzene.Result[json.RawMessage] {
	payload := result.ResultPayload()
	if payload == nil {
		return benzene.Result[json.RawMessage]{Status: result.ResultStatus(), Errors: result.ResultErrors()}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return benzene.UnexpectedError[json.RawMessage]("inprocess: failed to serialize handler payload: " + err.Error())
	}
	rawMessage := json.RawMessage(raw)
	return benzene.Result[json.RawMessage]{Status: result.ResultStatus(), Errors: result.ResultErrors(), Payload: &rawMessage}
}
