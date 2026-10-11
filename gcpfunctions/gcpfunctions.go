package gcpfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/types"

	benzene "github.com/daniellepelley/benzene-go"
	benzenece "github.com/daniellepelley/benzene-go/cloudevents"
	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/httpbinding"
	"github.com/daniellepelley/benzene-go/wire"
)

// Compile-time proof the two Functions Framework registration functions still have the
// signatures this package builds against; if the framework changes either, the build fails here
// rather than at some subtle call site.
var (
	_ func(string, func(http.ResponseWriter, *http.Request))       = functions.HTTP
	_ func(string, func(context.Context, cloudevents.Event) error) = functions.CloudEvent
)

// RegisterHTTP registers an HTTP-triggered Cloud Function (2nd gen) named name that serves a
// native httpbinding.Handler built from builder and routes. Call it from an init function (or
// any code that runs before the framework starts serving); the deployed function is then
// selected with the FUNCTION_TARGET=name environment variable, the standard Functions Framework
// convention.
//
// It is a thin pass-through: routing, DI scope per request, status mapping, and response headers
// are all httpbinding.Handler's, unchanged - the only thing this adds is registering that handler
// with the framework under name.
func RegisterHTTP(name string, builder *benzene.ApplicationBuilder, routes []httpbinding.Route) {
	functions.HTTP(name, httpbinding.Handler(builder, routes).ServeHTTP)
}

// Option configures RegisterCloudEvent. The zero set of options inherits builder.ReservedNames
// and installs no failure hook.
type Option func(*cloudEventConfig)

type cloudEventConfig struct {
	names      wire.ReservedNames
	settlement envelope.Settlement
	onFailure  func(ctx context.Context, e cloudevents.Event, resp wire.Response)
}

// WithReservedNames overrides the reserved metadata names (wire-contracts.md §2) this function
// uses. RegisterCloudEvent defaults to builder.ReservedNames, so an application that configured
// the builder's names once already gets them; pass this only to override for a single function.
//
// The names matter when a CloudEvent carries no type of its own: the topic is then resolved from
// the CloudEvent extension attribute under the configured topic key (default "topic"), the same
// metadata-topic fallback the queue bindings use.
func WithReservedNames(names wire.ReservedNames) Option {
	return func(c *cloudEventConfig) { c.names = names }
}

// WithSettlement sets how the function settles a CloudEvent whose dispatch failed
// (transport-bindings.md §1, obligation 8). The default - the zero envelope.Settlement - returns an
// error so the platform retries it; envelope.AtMostOnce returns nil whatever the outcome, and
// IgnoreUnmatchedTopics returns nil for an event whose type no handler serves - for a trigger that
// delivers other services' events too.
func WithSettlement(settlement envelope.Settlement) Option {
	return func(c *cloudEventConfig) { c.settlement = settlement }
}

// WithOnFailure installs a hook called whenever a delivered CloudEvent's dispatch produces an
// unsuccessful Benzene result - whether the function then returns an error for the platform to
// retry (the default) or the settlement accepts the failure. The hook receives the original event
// and the wire.Response so it can log or meter the failure; it does not change the outcome. It is
// not called for a successful dispatch.
func WithOnFailure(hook func(ctx context.Context, e cloudevents.Event, resp wire.Response)) Option {
	return func(c *cloudEventConfig) { c.onFailure = hook }
}

// RegisterCloudEvent registers a CloudEvent-triggered Cloud Function (2nd gen) named name that
// dispatches each delivered CloudEvent through builder's pipeline. Call it before the framework
// starts serving; the deployed function is selected with FUNCTION_TARGET=name.
//
// A CloudEvent function acknowledges the delivery by returning nil and requests a retry by
// returning an error (Eventarc/Pub/Sub redeliver on a returned error, subject to the trigger's
// retry policy). Accordingly a dispatch whose Benzene result is successful returns nil, and an
// unsuccessful one returns an error - so a failure is retried by the platform, never silently
// dropped; WithSettlement offers the at-most-once override and unmatched-topic-as-ignored. The unit
// of settlement is one event. Deliveries are at-least-once, so handlers must be idempotent.
func RegisterCloudEvent(name string, builder *benzene.ApplicationBuilder, opts ...Option) {
	cfg := cloudEventConfig{names: builder.ReservedNames}
	for _, opt := range opts {
		opt(&cfg)
	}
	functions.CloudEvent(name, cloudEventHandler(builder, cfg))
}

// cloudEventHandler builds the framework handler RegisterCloudEvent registers: it binds builder
// and the resolved config to dispatchCloudEvent. Factored out so the binding is testable without
// registering with (or being invoked by) the live framework.
func cloudEventHandler(builder *benzene.ApplicationBuilder, cfg cloudEventConfig) func(context.Context, cloudevents.Event) error {
	return func(ctx context.Context, e cloudevents.Event) error {
		return dispatchCloudEvent(ctx, e, builder, cfg)
	}
}

// dispatchCloudEvent is the conversion-and-dispatch core RegisterCloudEvent installs, factored
// out so it is testable directly against a hand-built event.Event. It maps the event onto a
// wire.Request, dispatches it, calls onFailure (if set) for an unsuccessful result, and returns an
// error when the settlement does not acknowledge the outcome - by default, for any unsuccessful
// result. It never panics: a malformed event resolves to a request RouterMiddleware turns into a
// failure result, i.e. a returned error, not a crash.
func dispatchCloudEvent(ctx context.Context, e cloudevents.Event, builder *benzene.ApplicationBuilder, cfg cloudEventConfig) error {
	req := requestFromEvent(e, cfg.names)
	outcome := cfg.settlement.Dispatch(ctx, builder.Pipeline, builder.Container, req)
	if !outcome.Successful && cfg.onFailure != nil {
		cfg.onFailure(ctx, e, outcome.Response)
	}
	if !outcome.Acknowledge {
		return fmt.Errorf("gcpfunctions: dispatch of CloudEvent type %q failed with status %s", e.Type(), outcome.Response.StatusCode)
	}
	return nil
}

// requestFromEvent maps a CloudEvents SDK event onto the wire envelope. It reuses the cloudevents
// package's ToRequest so the mapping is identical to this port's other CloudEvents surface: type
// -> topic, data -> body (verbatim JSON), and every other attribute -> a "ce-"-prefixed header
// (ce-id, ce-source, ce-subject, extension foo -> ce-foo). When the event carries no type of its
// own, the topic falls back to the extension attribute under the configured topic key so a
// non-CloudEvents-native producer can still route; an unresolved topic stays empty, which
// RouterMiddleware maps to validation-error (a failure, hence a retry - never a silent drop).
func requestFromEvent(e cloudevents.Event, names wire.ReservedNames) wire.Request {
	ce := benzenece.Event{
		SpecVersion:     e.SpecVersion(),
		ID:              e.ID(),
		Source:          e.Source(),
		Type:            e.Type(),
		DataContentType: e.DataContentType(),
		DataSchema:      e.DataSchema(),
		Subject:         e.Subject(),
		Extensions:      stringExtensions(e.Extensions()),
	}
	if t := e.Time(); !t.IsZero() {
		ce.Time = t.UTC().Format(time.RFC3339Nano)
	}
	if data := e.Data(); len(data) > 0 {
		ce.Data = json.RawMessage(data)
	}

	req := benzenece.ToRequest(ce)
	if req.Topic == "" {
		if topic, ok := ce.Extensions[strings.ToLower(names.Topic())]; ok {
			req.Topic = topic
		}
	}
	return req
}

// stringExtensions renders the SDK's typed extension-attribute values as the canonical CloudEvents
// string forms the cloudevents package expects (extension names are lower-cased, matching the SDK's
// own normalization). A value that is somehow not a valid CloudEvents attribute type is rendered
// with a plain fmt fallback rather than dropped, so nothing an event carried is silently lost.
func stringExtensions(extensions map[string]interface{}) map[string]string {
	if len(extensions) == 0 {
		return nil
	}
	out := make(map[string]string, len(extensions))
	for name, value := range extensions {
		text, err := types.Format(value)
		if err != nil {
			text = fmt.Sprintf("%v", value)
		}
		out[strings.ToLower(name)] = text
	}
	return out
}
