package grpcbinding

import (
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/internal/transportmetadata"
	"github.com/daniellepelley/benzene-go/wire"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestConformance_TransportMetadata_TopicSource is the grpc entry of transport-metadata-cases.json's
// topicSources: the topic is the route claimed by the called method, never a `topic` metadata key.
func TestConformance_TransportMetadata_TopicSource(t *testing.T) {
	builder, recorder := transportmetadata.NewBuilder(t, wire.ReservedNames{})
	routes := []Route{{Method: greetMethod, Topic: benzene.NewTopic(transportmetadata.Topic), NewResponse: func() proto.Message { return &structpb.Struct{} }}}
	conn := newTestServer(t, UnaryServerInterceptor(builder, routes))

	req, err := structpb.NewStruct(map[string]any{"name": "conformance"})
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	ctx := metadata.AppendToOutgoingContext(withTimeout(t), wire.DefaultTopicKey, "not-the-topic")
	_ = conn.Invoke(ctx, greetMethod, req, &structpb.Struct{})

	if got := recorder.Only(t).Topic; got != transportmetadata.Topic {
		t.Errorf("topic = %q, want %q (from the called method)", got, transportmetadata.Topic)
	}
}
