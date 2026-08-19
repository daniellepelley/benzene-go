package wire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRequest_RoundTrip(t *testing.T) {
	original := Request{
		Topic:   "order:create",
		Headers: map[string]string{"x-correlation-id": "abc", "traceparent": "00-..."},
		Body:    `{"name":"widget"}`,
	}

	data, err := MarshalRequest(original)
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}

	got, err := UnmarshalRequest(data)
	if err != nil {
		t.Fatalf("UnmarshalRequest() error = %v", err)
	}
	if got.Topic != original.Topic || got.Body != original.Body {
		t.Errorf("got = %+v, want %+v", got, original)
	}
	if got.Headers["x-correlation-id"] != "abc" {
		t.Errorf("Headers[x-correlation-id] = %q, want %q", got.Headers["x-correlation-id"], "abc")
	}
}

func TestRequest_WireFieldNamesAreCamelCase(t *testing.T) {
	data, err := MarshalRequest(Request{Topic: "t", Headers: map[string]string{}, Body: "b"})
	if err != nil {
		t.Fatalf("MarshalRequest() error = %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"topic", "headers", "body"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("marshaled Request is missing camelCase field %q: %s", field, data)
		}
	}
}

func TestResponse_RoundTrip(t *testing.T) {
	original := Response{
		StatusCode: "ok",
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       `{"message":"hi"}`,
	}

	data, err := MarshalResponse(original)
	if err != nil {
		t.Fatalf("MarshalResponse() error = %v", err)
	}

	got, err := UnmarshalResponse(data)
	if err != nil {
		t.Fatalf("UnmarshalResponse() error = %v", err)
	}
	if got.StatusCode != original.StatusCode || got.Body != original.Body {
		t.Errorf("got = %+v, want %+v", got, original)
	}
}

func TestResponse_WireFieldNamesAreCamelCase(t *testing.T) {
	data, err := MarshalResponse(Response{StatusCode: "ok", Headers: map[string]string{}, Body: "b"})
	if err != nil {
		t.Fatalf("MarshalResponse() error = %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"statusCode", "headers", "body"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("marshaled Response is missing camelCase field %q: %s", field, data)
		}
	}
}

func TestErrorPayload_RoundTrip(t *testing.T) {
	original := NewErrorPayload("not-found", []string{"no handler found for topic order:create"})

	data, err := MarshalErrorPayload(original)
	if err != nil {
		t.Fatalf("MarshalErrorPayload() error = %v", err)
	}

	got, err := UnmarshalErrorPayload(data)
	if err != nil {
		t.Fatalf("UnmarshalErrorPayload() error = %v", err)
	}
	// reflect.DeepEqual rather than ==: the struct now holds a slice, so it is not comparable.
	if !reflect.DeepEqual(got, original) {
		t.Errorf("got = %+v, want %+v", got, original)
	}
}

// TestNewErrorPayload_IsARealProblemDocument pins the §1.3 shape: the registry type and title for
// the status, benzeneStatus as the transport-neutral discriminator, detail as the joined
// compatibility member, and errors listing each message individually and in order.
func TestNewErrorPayload_IsARealProblemDocument(t *testing.T) {
	payload := NewErrorPayload("validation-error", []string{"first error", "second error"})

	if payload.Type != ProblemBase+"validation-error" {
		t.Errorf("Type = %q, want the §3.1 registry URI", payload.Type)
	}
	if payload.Title != "Validation failed" {
		t.Errorf("Title = %q, want the registry title", payload.Title)
	}
	if payload.BenzeneStatus != "validation-error" {
		t.Errorf("BenzeneStatus = %q, want the Benzene status string", payload.BenzeneStatus)
	}
	if payload.Detail != "first error, second error" {
		t.Errorf("Detail = %q, want the messages joined with \", \"", payload.Detail)
	}
	if len(payload.Errors) != 2 || payload.Errors[0].Message != "first error" || payload.Errors[1].Message != "second error" {
		t.Errorf("Errors = %+v, want both messages in order", payload.Errors)
	}
}

// TestNewErrorPayload_OmitsStatusWhereThereIsNoHTTPResponse pins the member that used to collide:
// RFC 9457's status is the integer HTTP code, so it must be absent - not zero, not null - on every
// transport that has no HTTP response (§1.3). An HTTP binding sets it (§4.1).
func TestNewErrorPayload_OmitsStatusWhereThereIsNoHTTPResponse(t *testing.T) {
	data, err := MarshalErrorPayload(NewErrorPayload("not-found", []string{"missing"}))
	if err != nil {
		t.Fatalf("MarshalErrorPayload() error = %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, present := raw["status"]; present {
		t.Errorf("status should be omitted where there is no HTTP response, got: %s", data)
	}
	if raw["benzeneStatus"] != "not-found" {
		t.Errorf("benzeneStatus = %v, want not-found: %s", raw["benzeneStatus"], data)
	}
}

// TestNewErrorPayload_ApplicationDefinedStatusGetsNoFabricatedType: §3.1 says an application's
// failure carries its own URI or none - the framework never invents one under benzene.app.
func TestNewErrorPayload_ApplicationDefinedStatusGetsNoFabricatedType(t *testing.T) {
	payload := NewErrorPayload("quota-exhausted", []string{"over limit"})

	if payload.Type != "" {
		t.Errorf("Type = %q, want empty for an application-defined status", payload.Type)
	}
	if payload.Title != "" {
		t.Errorf("Title = %q, want empty for an application-defined status", payload.Title)
	}
	if payload.BenzeneStatus != "quota-exhausted" {
		t.Errorf("BenzeneStatus = %q, want the application's status verbatim", payload.BenzeneStatus)
	}
}

// TestErrorPayload_Messages_PrefersErrorsOverDetail pins the withdrawal of the old "split detail on
// ', '" rule: errors is authoritative and ordered when present, and detail is ONE opaque message
// when it is not - never split, because messages contain commas.
func TestErrorPayload_Messages_PrefersErrorsOverDetail(t *testing.T) {
	withErrors := ErrorPayload{
		Detail: "ignored, because errors wins",
		Errors: []ProblemError{{Message: "first, with a comma"}, {Message: "second"}},
	}
	if got := withErrors.Messages(); !reflect.DeepEqual(got, []string{"first, with a comma", "second"}) {
		t.Errorf("Messages() = %q, want the errors array verbatim", got)
	}

	detailOnly := ErrorPayload{Detail: "one message, containing a comma"}
	if got := detailOnly.Messages(); !reflect.DeepEqual(got, []string{"one message, containing a comma"}) {
		t.Errorf("Messages() = %q, want detail as ONE opaque message", got)
	}

	if got := (ErrorPayload{}).Messages(); len(got) != 0 {
		t.Errorf("Messages() = %q, want none for an empty payload", got)
	}
}

func TestErrorPayload_OptionalMembersOmittedWhenEmpty(t *testing.T) {
	data, err := MarshalErrorPayload(ErrorPayload{BenzeneStatus: "not-found", Detail: "missing"})
	if err != nil {
		t.Fatalf("MarshalErrorPayload() error = %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"type", "title", "instance", "status", "errors"} {
		if _, ok := raw[field]; ok {
			t.Errorf("optional field %q should be omitted when empty, got: %s", field, data)
		}
	}
}

func TestErrorPayload_OptionalMembersPresentWhenSet(t *testing.T) {
	httpStatus := 404
	data, err := MarshalErrorPayload(ErrorPayload{
		BenzeneStatus: "not-found", Detail: "missing", Type: "about:blank", Title: "Not Found",
		Instance: "/orders/123", Status: &httpStatus,
	})
	if err != nil {
		t.Fatalf("MarshalErrorPayload() error = %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for field, want := range map[string]string{"type": "about:blank", "title": "Not Found", "instance": "/orders/123"} {
		if got := raw[field]; got != want {
			t.Errorf("field %q = %v, want %q", field, got, want)
		}
	}
	if raw["status"] != float64(404) {
		t.Errorf("status = %v, want 404 as a number", raw["status"])
	}
}

func TestUnmarshalRequest_MalformedJSON(t *testing.T) {
	if _, err := UnmarshalRequest([]byte("{not valid")); err == nil {
		t.Error("UnmarshalRequest() should return an error for malformed JSON")
	}
}

func TestUnmarshalResponse_MalformedJSON(t *testing.T) {
	if _, err := UnmarshalResponse([]byte("{not valid")); err == nil {
		t.Error("UnmarshalResponse() should return an error for malformed JSON")
	}
}

func TestUnmarshalErrorPayload_MalformedJSON(t *testing.T) {
	if _, err := UnmarshalErrorPayload([]byte("{not valid")); err == nil {
		t.Error("UnmarshalErrorPayload() should return an error for malformed JSON")
	}
}

func TestUnmarshalRequest_CaseInsensitivePropertyMatching(t *testing.T) {
	// wire-contracts.md §6: "Reading: property-name matching is case-insensitive."
	got, err := UnmarshalRequest([]byte(`{"TOPIC":"order:create","Headers":{},"BODY":"{}"}`))
	if err != nil {
		t.Fatalf("UnmarshalRequest() error = %v", err)
	}
	if got.Topic != "order:create" || got.Body != "{}" {
		t.Errorf("got = %+v, want Topic=order:create Body={}", got)
	}
}
