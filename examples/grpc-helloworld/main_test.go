package main

import (
	"context"
	"net"
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/benzenetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

// These tests boot the real app from its composition root (newApp) via benzenetest and push a
// native gRPC call in the front door - the same shape every transport's example uses. gRPC has no
// benzenetest.Send* helper (unlike HTTP/SQS/Pub-Sub), so the front door here is a real in-memory
// *grpc.Server over bufconn wearing the same interceptor main() installs; only the transport plumbing
// differs, the app and assertions are identical to helloworld's.

// newTestConn starts newServer over an in-memory bufconn listener (no TCP port, no credentials) and
// returns a connected *grpc.ClientConn. Using a live server, not calling the interceptor directly,
// exercises the full wire round trip: proto marshal, transport, grpc-go's codec, the interceptor,
// dispatch, and the response back.
func newTestConn(t *testing.T, opts ...benzenetest.Option) *grpc.ClientConn {
	t.Helper()
	host := benzenetest.NewHost(newApp(), opts...)

	lis := bufconn.Listen(1024 * 1024)
	server := newServer(host.Builder())
	go server.Serve(lis)
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func withTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestGreet_RoundTripsThroughTheClient(t *testing.T) {
	conn := newTestConn(t)

	resp, status, err := greet(withTimeout(t), conn, "World")
	if err != nil {
		t.Fatalf("greet() error = %v", err)
	}
	if status != benzene.StatusOk {
		t.Errorf("status = %q, want %q", status, benzene.StatusOk)
	}
	if resp.Greeting != "Hello, World, this is Benzene" {
		t.Errorf("Greeting = %q, want %q", resp.Greeting, "Hello, World, this is Benzene")
	}
}

func TestGreet_UsesTheOverriddenGreeterAdapter(t *testing.T) {
	// The whole point of the port interface: swap the adapter (here for a test spy) without
	// touching the handler. WithServices lands the override after ConfigureServices, before the
	// pipeline is built - last registration wins.
	conn := newTestConn(t, benzenetest.WithServices(func(b *benzene.ApplicationBuilder) {
		benzene.AddSingleton(b.Container, greeterKey, func(_ *benzene.Scope) Greeter { return shoutingGreeter{} })
	}))

	resp, _, err := greet(withTimeout(t), conn, "World")
	if err != nil {
		t.Fatalf("greet() error = %v", err)
	}
	if resp.Greeting != "HELLO, WORLD" {
		t.Errorf("Greeting = %q, want %q - the overridden adapter should have run", resp.Greeting, "HELLO, WORLD")
	}
}

func TestSayHello_FailureCarriesBenzeneStatusTrailer(t *testing.T) {
	conn := newTestConn(t)

	// An empty name is a BadRequest from the handler; over gRPC that becomes a status.Error, and
	// the precise Benzene status rides the mandatory "benzene-status" trailer.
	req, err := structpb.NewStruct(map[string]any{"name": ""})
	if err != nil {
		t.Fatalf("structpb.NewStruct() error = %v", err)
	}
	var trailer metadata.MD
	resp := &structpb.Struct{}
	err = conn.Invoke(withTimeout(t), greetMethod, req, resp, grpc.Trailer(&trailer))
	if err == nil {
		t.Fatal("Invoke() error = nil, want an RPC error for a bad request")
	}
	if _, ok := grpcstatus.FromError(err); !ok {
		t.Fatalf("Invoke() error is not a gRPC status: %v", err)
	}
	if got := trailer.Get("benzene-status"); len(got) == 0 || got[len(got)-1] != string(benzene.StatusBadRequest) {
		t.Errorf("benzene-status trailer = %v, want %q", got, benzene.StatusBadRequest)
	}
}

// shoutingGreeter is the test spy adapter proving the port is swappable.
type shoutingGreeter struct{}

func (shoutingGreeter) Greet(name string) string {
	upper := ""
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		upper += string(r)
	}
	return "HELLO, " + upper
}
