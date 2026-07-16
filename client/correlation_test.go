package client

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func okSender() Sender {
	return SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})
}

func TestCorrelationDecorator_InjectsGeneratedIDWhenAbsent(t *testing.T) {
	var seenHeaders map[string]string
	captor := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		seenHeaders = headers
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	decorated := CorrelationDecorator(captor, func() string { return "fixed-id" })
	decorated.Send(context.Background(), benzene.NewTopic("t"), map[string]string{}, nil)

	if seenHeaders["x-correlation-id"] != "fixed-id" {
		t.Errorf(`headers["x-correlation-id"] = %q, want "fixed-id"`, seenHeaders["x-correlation-id"])
	}
}

func TestCorrelationDecorator_PreservesExistingHeaderCaseInsensitively(t *testing.T) {
	var seenHeaders map[string]string
	captor := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		seenHeaders = headers
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	decorated := CorrelationDecorator(captor, func() string { return "should-not-be-used" })
	original := map[string]string{"X-Correlation-Id": "caller-provided"}
	decorated.Send(context.Background(), benzene.NewTopic("t"), original, nil)

	if seenHeaders["X-Correlation-Id"] != "caller-provided" {
		t.Errorf(`headers["X-Correlation-Id"] = %q, want "caller-provided"`, seenHeaders["X-Correlation-Id"])
	}
	if _, ok := seenHeaders["x-correlation-id"]; ok {
		t.Error("decorator should not add a second, lower-case correlation header")
	}
}

func TestCorrelationDecorator_DoesNotMutateCallersHeaderMap(t *testing.T) {
	decorated := CorrelationDecorator(okSender(), func() string { return "generated" })
	original := map[string]string{"other": "value"}

	decorated.Send(context.Background(), benzene.NewTopic("t"), original, nil)

	if _, ok := original["x-correlation-id"]; ok {
		t.Error("the caller's own headers map should not be mutated in place")
	}
	if len(original) != 1 {
		t.Errorf("original map = %v, want unchanged with 1 entry", original)
	}
}

func TestCorrelationDecorator_NilHeadersIsHandled(t *testing.T) {
	var seenHeaders map[string]string
	captor := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		seenHeaders = headers
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	decorated := CorrelationDecorator(captor, func() string { return "generated" })
	decorated.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if seenHeaders["x-correlation-id"] != "generated" {
		t.Errorf(`headers["x-correlation-id"] = %q, want "generated"`, seenHeaders["x-correlation-id"])
	}
}

func TestCorrelationDecorator_NilGenerateUsesDefault(t *testing.T) {
	var seenHeaders map[string]string
	captor := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		seenHeaders = headers
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	decorated := CorrelationDecorator(captor, nil)
	decorated.Send(context.Background(), benzene.NewTopic("t"), map[string]string{}, nil)

	matched, err := regexp.MatchString("^[0-9a-f]{32}$", seenHeaders["x-correlation-id"])
	if err != nil {
		t.Fatalf("regexp error = %v", err)
	}
	if !matched {
		t.Errorf("x-correlation-id = %q, want a 32-character lowercase hex string", seenHeaders["x-correlation-id"])
	}
}

func TestDefaultCorrelationID_GeneratesDistinctValues(t *testing.T) {
	first := defaultCorrelationID()
	second := defaultCorrelationID()
	if first == second {
		t.Error("defaultCorrelationID() returned the same value twice in a row")
	}
}
