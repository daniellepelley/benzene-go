package azurefunctions

import (
	"net/http"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
)

var (
	atMostOnce      = WithSettlement(envelope.Settlement{Delivery: envelope.AtMostOnce})
	ignoreUnmatched = WithSettlement(envelope.Settlement{IgnoreUnmatchedTopics: true})
)

// transport-bindings.md §1 obligation 8: each one-way trigger fails the invocation (outer 500) on a
// failure by default, acknowledges it (outer 200) under at-most-once, and acknowledges an unmatched
// topic - only - under IgnoreUnmatchedTopics.
func TestOneWayTriggers_Settlement(t *testing.T) {
	failingQueue := `{"Data": {"mySbMsg": "{\"name\":\"\"}"}, "Metadata": {"UserProperties": {"topic": "greet"}}}`
	unmatchedQueue := `{"Data": {"mySbMsg": "{}"}, "Metadata": {"UserProperties": {"topic": "someone:elses"}}}`
	failingEventGrid := `{"Data": {"event": {"id":"1","eventType":"greet","subject":"s","data":{"name":""},"dataVersion":"1","eventTime":"2026-01-01T00:00:00Z","topic":"t"}}}`
	unmatchedEventGrid := `{"Data": {"event": {"id":"1","eventType":"someone:elses","subject":"s","data":{},"dataVersion":"1","eventTime":"2026-01-01T00:00:00Z","topic":"t"}}}`
	failingEventHub := `{"Data": {"eventHubMessages": ["{\"name\":\"\"}", "{\"name\":\"Later\"}"]}, "Metadata": {"PropertiesArray": [{"topic": "greet"}, {"topic": "greet"}]}}`
	unmatchedEventHub := `{"Data": {"eventHubMessages": ["{}"]}, "Metadata": {"PropertiesArray": [{"topic": "someone:elses"}]}}`
	failingCosmos := `{"Data": {"documents": [{"id": ""}]}}`
	tick := `{"Data": {"myTimer": {"IsPastDue": false}}}`

	tests := []struct {
		name    string
		handler func(...Option) http.Handler
		invoke  func(*testing.T, http.Handler, string) int
		payload string
		opts    []Option
		want    int
	}{
		{"queue failure is redelivered", queue(t), viaQueue, failingQueue, nil, http.StatusInternalServerError},
		{"queue failure under at-most-once", queue(t), viaQueue, failingQueue, []Option{atMostOnce}, http.StatusOK},
		{"queue unmatched topic is redelivered", queue(t), viaQueue, unmatchedQueue, nil, http.StatusInternalServerError},
		{"queue unmatched topic ignored on request", queue(t), viaQueue, unmatchedQueue, []Option{ignoreUnmatched}, http.StatusOK},
		{"queue failure is not an unmatched topic", queue(t), viaQueue, failingQueue, []Option{ignoreUnmatched}, http.StatusInternalServerError},

		{"event grid failure is redelivered", eventGrid(t), viaEventGrid, failingEventGrid, nil, http.StatusInternalServerError},
		{"event grid failure under at-most-once", eventGrid(t), viaEventGrid, failingEventGrid, []Option{atMostOnce}, http.StatusOK},
		{"event grid unmatched topic ignored on request", eventGrid(t), viaEventGrid, unmatchedEventGrid, []Option{ignoreUnmatched}, http.StatusOK},

		{"event hub failure redelivers the batch", eventHub(t), viaEventHub, failingEventHub, nil, http.StatusInternalServerError},
		{"event hub failure under at-most-once", eventHub(t), viaEventHub, failingEventHub, []Option{atMostOnce}, http.StatusOK},
		{"event hub unmatched topic ignored on request", eventHub(t), viaEventHub, unmatchedEventHub, []Option{ignoreUnmatched}, http.StatusOK},

		{"cosmos failure redelivers the batch", cosmos(t, "orders:changed"), viaCosmos, failingCosmos, nil, http.StatusInternalServerError},
		{"cosmos failure under at-most-once", cosmos(t, "orders:changed"), viaCosmos, failingCosmos, []Option{atMostOnce}, http.StatusOK},
		{"cosmos code-named topic with no handler ignored on request", cosmos(t, "nobody:serves"), viaCosmos, failingCosmos, []Option{ignoreUnmatched}, http.StatusOK},

		{"timer failure fails the invocation", timer(t, true), viaTimer, tick, nil, http.StatusInternalServerError},
		{"timer failure under at-most-once", timer(t, true), viaTimer, tick, []Option{atMostOnce}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.invoke(t, tt.handler(tt.opts...), tt.payload); got != tt.want {
				t.Errorf("outer status = %d, want %d", got, tt.want)
			}
		})
	}
}

func queue(t *testing.T) func(...Option) http.Handler {
	builder := newTestBuilder(t)
	return func(opts ...Option) http.Handler { return QueueHandler(builder, "mySbMsg", opts...) }
}

func eventGrid(t *testing.T) func(...Option) http.Handler {
	builder := newTestBuilder(t)
	return func(opts ...Option) http.Handler { return EventGridHandler(builder, "event", opts...) }
}

func eventHub(t *testing.T) func(...Option) http.Handler {
	builder := newEventHubBuilder(t, nil)
	return func(opts ...Option) http.Handler { return EventHubHandler(builder, "eventHubMessages", opts...) }
}

func cosmos(t *testing.T, topic string) func(...Option) http.Handler {
	builder := newCosmosBuilder(t, nil)
	return func(opts ...Option) http.Handler {
		return CosmosHandler(builder, benzene.NewTopic(topic), "documents", opts...)
	}
}

func timer(t *testing.T, fail bool) func(...Option) http.Handler {
	builder := newTimerBuilder(t, nil, fail)
	return func(opts ...Option) http.Handler {
		return TimerHandler(builder, benzene.NewTopic("nightly-cleanup"), "myTimer", opts...)
	}
}

func viaQueue(t *testing.T, h http.Handler, p string) int     { return invokeQueue(t, h, p).Code }
func viaEventGrid(t *testing.T, h http.Handler, p string) int { return invokeEventGrid(t, h, p).Code }
func viaEventHub(t *testing.T, h http.Handler, p string) int  { return invokeEventHub(t, h, p).Code }
func viaCosmos(t *testing.T, h http.Handler, p string) int    { return invokeCosmos(t, h, p).Code }
func viaTimer(t *testing.T, h http.Handler, p string) int     { return invokeTimer(t, h, p).Code }
