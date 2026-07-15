package wire

import (
	"encoding/json"
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
		StatusCode: "Ok",
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
	data, err := MarshalResponse(Response{StatusCode: "Ok", Headers: map[string]string{}, Body: "b"})
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
	original := ErrorPayload{Status: "NotFound", Detail: "no handler found for topic order:create"}

	data, err := MarshalErrorPayload(original)
	if err != nil {
		t.Fatalf("MarshalErrorPayload() error = %v", err)
	}

	got, err := UnmarshalErrorPayload(data)
	if err != nil {
		t.Fatalf("UnmarshalErrorPayload() error = %v", err)
	}
	if got != original {
		t.Errorf("got = %+v, want %+v", got, original)
	}
}

func TestErrorPayload_ReservedFieldsOmittedWhenEmpty(t *testing.T) {
	data, err := MarshalErrorPayload(ErrorPayload{Status: "NotFound", Detail: "missing"})
	if err != nil {
		t.Fatalf("MarshalErrorPayload() error = %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"type", "title", "instance"} {
		if _, ok := raw[field]; ok {
			t.Errorf("reserved field %q should be omitted when empty, got: %s", field, data)
		}
	}
}

func TestErrorPayload_ReservedFieldsPresentWhenSet(t *testing.T) {
	data, err := MarshalErrorPayload(ErrorPayload{
		Status: "NotFound", Detail: "missing", Type: "about:blank", Title: "Not Found", Instance: "/orders/123",
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
