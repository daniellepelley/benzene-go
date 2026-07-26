// Package cloudevents maps the Benzene wire envelope onto CloudEvents 1.0
// (https://github.com/cloudevents/spec) - the CNCF-graduated cross-cloud event format that
// AWS EventBridge, Azure Event Grid, Knative, and most modern event routers can emit or
// carry. It needs no third-party dependency: the JSON format and the HTTP protocol binding
// are both "just" JSON and headers.
//
// The mapping is deliberately small and symmetric:
//
//   - CloudEvents `type` <-> the Benzene topic (both answer "which handler is this for").
//   - CloudEvents `data` <-> the wire body (JSON kept verbatim; see Event.body).
//   - Every other context attribute maps to a wire header prefixed "ce-": id -> ce-id,
//     source -> ce-source, extension foo -> ce-foo. In the outbound direction only "ce-"
//     headers map back (ce-foo -> extension foo, when foo is a legal extension name);
//     unprefixed wire headers have no CloudEvents equivalent and are dropped - documented
//     lossiness, not an accident (put values that must travel in the payload, or name the
//     header ce-<legalname> to opt it in).
//
// Handler is the inbound entry point: an http.Handler for whatever pushes CloudEvents over
// HTTP at you (an Event Grid subscription, a Knative trigger, an EventBridge API
// destination), accepting both HTTP content modes - binary (ce-* headers, body = data) and
// structured (application/cloudevents+json) - and answering with the same ack/nack contract
// as the queue bindings: 204 for a success status, 500 for a non-success one (the sender's
// own retry machinery takes over), 400 for a delivery that isn't a valid CloudEvent at all.
package cloudevents

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/daniellepelley/benzene-go/wire"
)

// Event is a CloudEvents 1.0 event in the JSON ("structured") representation. Extensions
// holds the extension attributes (top-level members beyond the specified ones) as strings -
// the CloudEvents type system's canonical string forms; non-string JSON scalars are
// stringified on parse, and JSON objects/arrays (not legal extension values) are skipped.
type Event struct {
	SpecVersion     string
	ID              string
	Source          string
	Type            string
	DataContentType string
	DataSchema      string
	Subject         string
	Time            string
	// Data is the raw JSON value of the "data" member (nil if absent). If the event instead
	// carried binary "data_base64", Data holds the decoded bytes re-encoded as a JSON string.
	// An event parsed from a binary-mode HTTP delivery holds the body verbatim, which may not
	// be JSON at all (a text/plain payload) - MarshalJSON re-quotes such data.
	Data       json.RawMessage
	Extensions map[string]string
}

// specifiedAttributes are the JSON member names the spec reserves - everything else at the
// top level is an extension.
var specifiedAttributes = map[string]bool{
	"specversion": true, "id": true, "source": true, "type": true,
	"datacontenttype": true, "dataschema": true, "subject": true, "time": true,
	"data": true, "data_base64": true,
}

// legalExtensionName reports whether name may be a CloudEvents extension attribute name:
// lowercase letters and digits only, non-empty, at most 20 characters (the spec's SHOULD,
// enforced here so emitted events are conservative), and not a specified attribute name.
func legalExtensionName(name string) bool {
	if name == "" || len(name) > 20 || specifiedAttributes[name] {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// ParseStructured parses a structured-mode (application/cloudevents+json) event. The spec's
// required attributes (specversion, id, source, type) must be present and specversion must
// be 1.x.
func ParseStructured(body []byte) (Event, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return Event{}, fmt.Errorf("cloudevents: malformed structured event: %w", err)
	}

	event := Event{Extensions: map[string]string{}}
	for name, raw := range members {
		switch name {
		case "specversion":
			stringMember(raw, &event.SpecVersion)
		case "id":
			stringMember(raw, &event.ID)
		case "source":
			stringMember(raw, &event.Source)
		case "type":
			stringMember(raw, &event.Type)
		case "datacontenttype":
			stringMember(raw, &event.DataContentType)
		case "dataschema":
			stringMember(raw, &event.DataSchema)
		case "subject":
			stringMember(raw, &event.Subject)
		case "time":
			stringMember(raw, &event.Time)
		case "data":
			event.Data = raw
		case "data_base64":
			var encoded string
			if err := json.Unmarshal(raw, &encoded); err != nil {
				return Event{}, errors.New("cloudevents: data_base64 is not a JSON string")
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return Event{}, errors.New("cloudevents: data_base64 is not valid base64")
			}
			quoted, err := json.Marshal(string(decoded))
			if err != nil {
				// A Go string always marshals; defensive only.
				return Event{}, errors.New("cloudevents: failed to re-encode data_base64")
			}
			event.Data = quoted
		default:
			if value, ok := scalarString(raw); ok {
				event.Extensions[name] = value
			}
		}
	}
	return event, validate(event)
}

// stringMember decodes raw into *dest when raw is a JSON string; a non-string value for a
// string-typed context attribute is left empty and caught by validate (for the required
// attributes) or dropped (for the optional ones) rather than failing the whole event.
func stringMember(raw json.RawMessage, dest *string) {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		*dest = value
	}
}

// scalarString renders a JSON scalar as its canonical string: strings unquoted, numbers and
// booleans as their literal text. Objects, arrays, and null - not legal extension attribute
// values - report ok=false.
func scalarString(raw json.RawMessage) (value string, ok bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return text, true
}

func validate(event Event) error {
	if event.SpecVersion == "" || !strings.HasPrefix(event.SpecVersion, "1.") {
		return fmt.Errorf("cloudevents: unsupported specversion %q", event.SpecVersion)
	}
	if event.Type == "" || event.ID == "" || event.Source == "" {
		return errors.New("cloudevents: missing required attribute (type, id, and source must be non-empty)")
	}
	return nil
}

// ToRequest maps an event onto the wire envelope: Topic from `type`, Body from `data`, and
// every other attribute as a "ce-"-prefixed header (see the package doc). data is kept
// verbatim as JSON text, except that a JSON-string data with a declared non-JSON
// datacontenttype is unquoted - it is a text payload, not a JSON document.
func ToRequest(event Event) wire.Request {
	headers := make(map[string]string, len(event.Extensions)+6)
	setHeader(headers, "ce-specversion", event.SpecVersion)
	setHeader(headers, "ce-id", event.ID)
	setHeader(headers, "ce-source", event.Source)
	setHeader(headers, "ce-datacontenttype", event.DataContentType)
	setHeader(headers, "ce-dataschema", event.DataSchema)
	setHeader(headers, "ce-subject", event.Subject)
	setHeader(headers, "ce-time", event.Time)
	for name, value := range event.Extensions {
		headers["ce-"+name] = value
	}

	body := string(event.Data)
	if event.DataContentType != "" && !strings.Contains(event.DataContentType, "json") {
		var text string
		if err := json.Unmarshal(event.Data, &text); err == nil {
			body = text
		}
	}

	return wire.Request{Topic: event.Type, Headers: headers, Body: body}
}

func setHeader(headers map[string]string, name, value string) {
	if value != "" {
		headers[name] = value
	}
}

// FromRequest maps a wire.Request onto an event for sending somewhere CloudEvents-shaped:
// `type` from the topic, `data` from the body (verbatim when the body is valid JSON, else as
// a JSON string with datacontenttype text/plain), and id/source from the arguments (the
// caller owns identity - this package won't invent one). Wire headers named "ce-<attr>" map
// back onto the matching attribute or extension; all other headers are dropped (see the
// package doc for why that lossiness is deliberate).
func FromRequest(req wire.Request, id, source string) Event {
	event := Event{
		SpecVersion:     "1.0",
		ID:              id,
		Source:          source,
		Type:            req.Topic,
		DataContentType: "application/json",
		Extensions:      map[string]string{},
	}

	if json.Valid([]byte(req.Body)) && strings.TrimSpace(req.Body) != "" {
		event.Data = json.RawMessage(req.Body)
	} else if req.Body != "" {
		quoted, err := json.Marshal(req.Body)
		if err == nil {
			// A Go string always marshals; the error branch is defensive only.
			event.Data = quoted
			event.DataContentType = "text/plain"
		}
	}

	for name, value := range req.Headers {
		attr, ok := strings.CutPrefix(strings.ToLower(name), "ce-")
		if !ok {
			continue
		}
		switch attr {
		case "specversion":
			// The emitted event is always 1.0; an inbound copy of the attribute is not an
			// extension to echo.
		case "id":
			// id/source are owned by the arguments - an inbound event's identity must not
			// leak onto a new event.
		case "source":
		case "datacontenttype":
			event.DataContentType = value
		case "dataschema":
			event.DataSchema = value
		case "subject":
			event.Subject = value
		case "time":
			event.Time = value
		default:
			if legalExtensionName(attr) {
				event.Extensions[attr] = value
			}
		}
	}
	return event
}

// MarshalJSON renders the event in the structured JSON representation - extensions as
// top-level members, data verbatim.
func (e Event) MarshalJSON() ([]byte, error) {
	members := make(map[string]any, len(e.Extensions)+9)
	members["specversion"] = e.SpecVersion
	members["id"] = e.ID
	members["source"] = e.Source
	members["type"] = e.Type
	if e.DataContentType != "" {
		members["datacontenttype"] = e.DataContentType
	}
	if e.DataSchema != "" {
		members["dataschema"] = e.DataSchema
	}
	if e.Subject != "" {
		members["subject"] = e.Subject
	}
	if e.Time != "" {
		members["time"] = e.Time
	}
	if e.Data != nil {
		if json.Valid(e.Data) {
			members["data"] = e.Data
		} else {
			members["data"] = string(e.Data)
		}
	}
	for name, value := range e.Extensions {
		if legalExtensionName(name) {
			members[name] = value
		}
	}
	return json.Marshal(members)
}
