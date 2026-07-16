package benzene

import (
	"context"
	"testing"
)

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

func TestInvocationContext_SetResponseHeaderLowerCasesAndOverwrites(t *testing.T) {
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	if ic.ResponseHeaders != nil {
		t.Fatal("ResponseHeaders should stay nil until the first set")
	}

	ic.SetResponseHeader("X-Request-Id", "first")
	ic.SetResponseHeader("x-request-id", "last")

	if got := ic.ResponseHeaders["x-request-id"]; got != "last" {
		t.Errorf(`ResponseHeaders["x-request-id"] = %q, want %q (lower-cased, last write wins)`, got, "last")
	}
	if len(ic.ResponseHeaders) != 1 {
		t.Errorf("ResponseHeaders = %v, want exactly one entry", ic.ResponseHeaders)
	}
}

func TestSetResponseHeader_ReachesInvocationViaContext(t *testing.T) {
	ic := NewInvocationContext(NewTopic("t"), nil, nil, nil)
	ctx := contextWithInvocation(context.Background(), ic)

	if ok := SetResponseHeader(ctx, "X-Custom", "value"); !ok {
		t.Fatal("SetResponseHeader() ok = false, want true when ctx carries an invocation")
	}
	if got := ic.ResponseHeaders["x-custom"]; got != "value" {
		t.Errorf(`ResponseHeaders["x-custom"] = %q, want %q`, got, "value")
	}
}

func TestSetResponseHeader_NoInvocationOnContextIsDropped(t *testing.T) {
	if ok := SetResponseHeader(context.Background(), "x-custom", "value"); ok {
		t.Error("SetResponseHeader() ok = true, want false when ctx carries no invocation")
	}
}
