package cloudevents

import (
	"errors"
	"io"
	"net/http"
	"strings"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/envelope"
)

// StructuredContentType is the structured-mode media type of the CloudEvents HTTP protocol
// binding; a request whose Content-Type starts with this carries the whole event as its
// body. Anything else with a ce-specversion header is binary mode.
const StructuredContentType = "application/cloudevents+json"

// Handler adapts builder into the http.Handler a CloudEvents-over-HTTP sender pushes to - an
// Azure Event Grid subscription (CloudEvents schema), a Knative trigger, an AWS EventBridge
// API destination, or any other source speaking the CloudEvents HTTP protocol binding. Both
// content modes are accepted: structured (application/cloudevents+json) and binary (ce-*
// headers with the data as the body). Batched mode (application/cloudevents-batch+json) is
// not implemented and answers 415.
//
// Each event gets its own pipeline invocation via envelope.Dispatch - topic from the event's
// `type` (see ToRequest for the full mapping). The response is the queue bindings' ack/nack
// contract: 204 for a success status; 500 with the wire error payload for a non-success one,
// which CloudEvents-speaking senders treat as "retry per your policy"; 400 for a request
// that isn't a valid CloudEvent at all.
func Handler(builder *benzene.ApplicationBuilder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		event, err := parseHTTP(r.Header, body)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errBatchNotSupported) {
				status = http.StatusUnsupportedMediaType
			}
			http.Error(w, err.Error(), status)
			return
		}

		resp := envelope.Dispatch(r.Context(), builder.Pipeline, builder.Container, ToRequest(event))
		if !benzene.Status(resp.StatusCode).IsSuccess() {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, resp.Body)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

var errBatchNotSupported = errors.New("cloudevents: batched mode (application/cloudevents-batch+json) is not supported")

// parseHTTP detects the content mode and parses accordingly.
func parseHTTP(header http.Header, body []byte) (Event, error) {
	contentType := header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/cloudevents-batch+json") {
		return Event{}, errBatchNotSupported
	}
	if strings.HasPrefix(contentType, StructuredContentType) {
		return ParseStructured(body)
	}
	if header.Get("ce-specversion") != "" {
		return ParseBinary(header, body)
	}
	return Event{}, errors.New("cloudevents: neither structured content type nor ce-specversion header present")
}

// ParseBinary parses a binary-mode HTTP delivery: context attributes from the ce-* headers,
// datacontenttype from Content-Type, data from the body verbatim. The same required
// attributes as ParseStructured are enforced.
func ParseBinary(header http.Header, body []byte) (Event, error) {
	event := Event{
		SpecVersion:     header.Get("ce-specversion"),
		ID:              header.Get("ce-id"),
		Source:          header.Get("ce-source"),
		Type:            header.Get("ce-type"),
		DataContentType: header.Get("Content-Type"),
		DataSchema:      header.Get("ce-dataschema"),
		Subject:         header.Get("ce-subject"),
		Time:            header.Get("ce-time"),
		Extensions:      map[string]string{},
	}
	for name, values := range header {
		attr, ok := strings.CutPrefix(strings.ToLower(name), "ce-")
		if !ok || len(values) == 0 || specifiedAttributes[attr] {
			continue
		}
		if legalExtensionName(attr) {
			event.Extensions[attr] = values[len(values)-1]
		}
	}
	if len(body) > 0 {
		event.Data = body
	}
	return event, validate(event)
}
