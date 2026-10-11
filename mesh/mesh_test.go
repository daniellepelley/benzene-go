package mesh

import (
	"context"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
)

type echoRequest struct {
	Text string `json:"text"`
}

type echoResponse struct {
	Text string `json:"text"`
}

func echoHandler(_ context.Context, req echoRequest) benzene.Result[echoResponse] {
	return benzene.Ok(echoResponse{Text: req.Text})
}

func newTestRegistry(t *testing.T, topics ...benzene.Topic) *benzene.Registry {
	t.Helper()
	registry := benzene.NewRegistry()
	for _, topic := range topics {
		if err := benzene.Register(registry, topic, benzene.Handler[echoRequest, echoResponse](echoHandler)); err != nil {
			t.Fatalf("Register(%v) error = %v", topic, err)
		}
	}
	return registry
}
