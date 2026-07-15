package benzene

import (
	"context"
	"encoding/json"
	"testing"
)

type helloRequest struct {
	Name string
}

type helloResponse struct {
	Message string
}

func helloHandler(_ context.Context, req helloRequest) Result[helloResponse] {
	return Ok(helloResponse{Message: "Hello " + req.Name})
}

func TestRegister_AndResolve(t *testing.T) {
	r := NewRegistry()
	topic := NewTopic("hello:world")

	if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if !r.Has(topic) {
		t.Fatal("Has() = false after Register()")
	}

	handler, ok := r.resolve(topic)
	if !ok {
		t.Fatal("resolve() = (_, false), want (_, true)")
	}

	result, err := handler(context.Background(), helloRequest{Name: "World"})
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if result.ResultStatus() != StatusOk {
		t.Errorf("ResultStatus() = %q, want %q", result.ResultStatus(), StatusOk)
	}
	payload, ok := result.ResultPayload().(helloResponse)
	if !ok || payload.Message != "Hello World" {
		t.Errorf("ResultPayload() = %v, want {Message: Hello World}", result.ResultPayload())
	}
}

func TestRegister_DuplicateTopicIsAStartupError(t *testing.T) {
	r := NewRegistry()
	topic := NewTopic("hello:world")

	if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err == nil {
		t.Fatal("second Register() for the same topic should return an error, got nil")
	}
}

func TestRegister_DifferentVersionsAreIndependent(t *testing.T) {
	r := NewRegistry()
	unversioned := NewTopic("hello:world")
	v2 := unversioned.WithVersion("v2")

	if err := Register(r, unversioned, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register(unversioned) error = %v", err)
	}
	if err := Register(r, v2, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register(v2) error = %v, want nil (different version is a distinct topic)", err)
	}
}

func TestResolve_UnregisteredTopic(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.resolve(NewTopic("nope")); ok {
		t.Error("resolve() of an unregistered topic should return ok = false")
	}
	if r.Has(NewTopic("nope")) {
		t.Error("Has() of an unregistered topic should be false")
	}
}

func TestErasedHandler_WrongRequestType(t *testing.T) {
	r := NewRegistry()
	topic := NewTopic("hello:world")
	if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	handler, _ := r.resolve(topic)
	_, err := handler(context.Background(), 42)
	if err == nil {
		t.Fatal("calling the erased handler with the wrong request type should return an error")
	}
}

func TestErasedHandler_JSONBytesConvertedToTypedRequest(t *testing.T) {
	r := NewRegistry()
	topic := NewTopic("hello:world")
	if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	handler, _ := r.resolve(topic)
	result, err := handler(context.Background(), []byte(`{"Name":"JSON"}`))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	payload, ok := result.ResultPayload().(helloResponse)
	if !ok || payload.Message != "Hello JSON" {
		t.Errorf("ResultPayload() = %v, want {Message: Hello JSON}", result.ResultPayload())
	}
}

func TestErasedHandler_MalformedJSONIsAnError(t *testing.T) {
	r := NewRegistry()
	topic := NewTopic("hello:world")
	if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	handler, _ := r.resolve(topic)
	if _, err := handler(context.Background(), []byte(`{not valid json`)); err == nil {
		t.Fatal("malformed JSON body should return an error")
	}
}

func TestConvertRequest_ZeroCopyPassthrough(t *testing.T) {
	want := helloRequest{Name: "Native"}
	got, err := convertRequest[helloRequest](want)
	if err != nil {
		t.Fatalf("convertRequest() error = %v", err)
	}
	if got != want {
		t.Errorf("convertRequest() = %+v, want %+v", got, want)
	}
}

func TestConvertRequest_JSONRawMessage(t *testing.T) {
	got, err := convertRequest[helloRequest](json.RawMessage(`{"Name":"Raw"}`))
	if err != nil {
		t.Fatalf("convertRequest() error = %v", err)
	}
	if got.Name != "Raw" {
		t.Errorf("convertRequest() = %+v, want Name=Raw", got)
	}
}
