package azurefunctions

import (
	"io"
	"net/http"

	"github.com/daniellepelley/benzene-go/envelope"
	"github.com/daniellepelley/benzene-go/wire"
)

// Option configures the one-way trigger handlers: QueueHandler, EventGridHandler, EventHubHandler,
// CosmosHandler and TimerHandler.
type Option func(*options)

type options struct {
	settlement envelope.Settlement
}

// WithSettlement sets how a one-way trigger handler settles an invocation whose dispatch failed
// (transport-bindings.md §1, obligation 8). The default - the zero envelope.Settlement - answers the
// host with outer HTTP 500, which marks the invocation failed and hands it to the trigger's own
// retry machinery (redelivery, then the poison queue or dead-letter destination);
// envelope.AtMostOnce answers 200 whatever the outcome, and IgnoreUnmatchedTopics answers 200 for a
// message whose topic no handler serves.
func WithSettlement(settlement envelope.Settlement) Option {
	return func(o *options) { o.settlement = settlement }
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// failInvocation answers the host with outer HTTP 500 - how a custom handler marks a trigger
// invocation failed - carrying the response envelope's body (the problem document) for the host's
// logs.
func failInvocation(w http.ResponseWriter, resp wire.Response) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	io.WriteString(w, resp.Body)
}
