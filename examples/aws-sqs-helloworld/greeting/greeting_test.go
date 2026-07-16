package greeting

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestHandler_ReturnsGreeting(t *testing.T) {
	result := Handler(context.Background(), GreetRequest{Name: "World"})

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	if result.Payload == nil || result.Payload.Greeting != "Hello, World!" {
		t.Errorf("Payload = %+v, want Greeting=Hello, World!", result.Payload)
	}
}

func TestHandler_MissingNameIsBadRequest(t *testing.T) {
	result := Handler(context.Background(), GreetRequest{Name: ""})

	if result.Status != benzene.StatusBadRequest {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusBadRequest)
	}
}
