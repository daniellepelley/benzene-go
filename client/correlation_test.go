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

func TestCorrelationDecorator_NilGenerateDoesNotFabricate(t *testing.T) {
	// With no generator the decorator propagates only - it must NOT invent a correlation value
	// the application never populated (wire-contracts.md §2: Benzene does not fabricate one).
	var seenHeaders map[string]string
	captor := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		seenHeaders = headers
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	decorated := CorrelationDecorator(captor, nil)
	decorated.Send(context.Background(), benzene.NewTopic("t"), map[string]string{}, nil)

	if got, ok := seenHeaders["x-correlation-id"]; ok {
		t.Errorf("x-correlation-id = %q, want it absent (the decorator must not fabricate one)", got)
	}
}

func TestCorrelationDecoratorWithKey_HonoursOverriddenHeaderName(t *testing.T) {
	var seenHeaders map[string]string
	captor := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		seenHeaders = headers
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	decorated := CorrelationDecoratorWithKey(captor, "x-my-correlation", func() string { return "cid-1" })
	decorated.Send(context.Background(), benzene.NewTopic("t"), map[string]string{}, nil)

	if seenHeaders["x-my-correlation"] != "cid-1" {
		t.Errorf(`headers["x-my-correlation"] = %q, want "cid-1"`, seenHeaders["x-my-correlation"])
	}
	if _, ok := seenHeaders["x-correlation-id"]; ok {
		t.Error("the default header should not be written when the key is overridden")
	}
}

func TestRandomCorrelationID_GeneratesDistinctHexValues(t *testing.T) {
	first := RandomCorrelationID()
	second := RandomCorrelationID()
	if matched, _ := regexp.MatchString("^[0-9a-f]{32}$", first); !matched {
		t.Errorf("RandomCorrelationID() = %q, want a 32-character lowercase hex string", first)
	}
	if first == second {
		t.Error("RandomCorrelationID() returned the same value twice in a row")
	}
}

func TestCorrelationDecorator_ExplicitGeneratorStartsAChain(t *testing.T) {
	// Passing a generator opts into edge-generation when the caller supplied none.
	var seenHeaders map[string]string
	captor := SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		seenHeaders = headers
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	decorated := CorrelationDecorator(captor, RandomCorrelationID)
	decorated.Send(context.Background(), benzene.NewTopic("t"), map[string]string{}, nil)

	if matched, _ := regexp.MatchString("^[0-9a-f]{32}$", seenHeaders["x-correlation-id"]); !matched {
		t.Errorf("x-correlation-id = %q, want a generated value", seenHeaders["x-correlation-id"])
	}
}
