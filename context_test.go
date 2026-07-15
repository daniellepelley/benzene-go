package benzene

import "testing"

func TestNewInvocationContext_NilHeadersBecomeEmptyMap(t *testing.T) {
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	if ic.Headers == nil {
		t.Fatal("Headers should never be nil, even when nil is passed in")
	}
	if len(ic.Headers) != 0 {
		t.Errorf("Headers = %v, want empty", ic.Headers)
	}
}

func TestNewInvocationContext_PreservesGivenHeaders(t *testing.T) {
	headers := map[string]string{"traceparent": "abc"}
	ic := NewInvocationContext(NewTopic("t"), headers, "request-body", nil)

	if ic.Headers["traceparent"] != "abc" {
		t.Errorf("Headers[traceparent] = %q, want %q", ic.Headers["traceparent"], "abc")
	}
	if ic.Request != "request-body" {
		t.Errorf("Request = %v, want %q", ic.Request, "request-body")
	}
	if ic.Result != nil {
		t.Error("Result should start nil until the pipeline dispatches a handler")
	}
}
