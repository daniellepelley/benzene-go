package client

import (
	"context"
	"encoding/json"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

func TestSenderFunc_ImplementsSenderByCallingItself(t *testing.T) {
	var called bool
	var f Sender = SenderFunc(func(ctx context.Context, topic benzene.Topic, headers map[string]string, message []byte) benzene.Result[json.RawMessage] {
		called = true
		return benzene.Result[json.RawMessage]{Status: benzene.StatusOk}
	})

	result := f.Send(context.Background(), benzene.NewTopic("t"), nil, nil)

	if !called {
		t.Error("SenderFunc.Send() should invoke the underlying function")
	}
	if result.Status != benzene.StatusOk {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
}
