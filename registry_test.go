package benzene

import (
	"context"
	"encoding/json"
	"reflect"
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

func TestRegistry_Topics(t *testing.T) {
	tests := []struct {
		name   string
		topics []Topic
		want   []Topic
	}{
		{
			name:   "empty registry",
			topics: nil,
			want:   []Topic{},
		},
		{
			name:   "sorted by id then version",
			topics: []Topic{NewTopic("b"), NewTopic("a").WithVersion("v2"), NewTopic("a"), NewTopic("a").WithVersion("v1")},
			want:   []Topic{NewTopic("a"), NewTopic("a").WithVersion("v1"), NewTopic("a").WithVersion("v2"), NewTopic("b")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			for _, topic := range tt.topics {
				if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
					t.Fatalf("Register(%v) error = %v", topic, err)
				}
			}

			got := r.Topics()
			if len(got) != len(tt.want) {
				t.Fatalf("Topics() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Topics()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRegistry_TopicTypes(t *testing.T) {
	r := NewRegistry()
	topic := NewTopic("hello:world")
	if err := Register(r, topic, Handler[helloRequest, helloResponse](helloHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	t.Run("returns the types captured at registration", func(t *testing.T) {
		request, response, ok := r.TopicTypes(topic)
		if !ok {
			t.Fatal("TopicTypes() ok = false for a registered topic")
		}
		if request != reflect.TypeOf(helloRequest{}) {
			t.Errorf("request = %v, want %v", request, reflect.TypeOf(helloRequest{}))
		}
		if response != reflect.TypeOf(helloResponse{}) {
			t.Errorf("response = %v, want %v", response, reflect.TypeOf(helloResponse{}))
		}
	})

	t.Run("reports ok=false for an unregistered topic", func(t *testing.T) {
		request, response, ok := r.TopicTypes(NewTopic("no:such:topic"))
		if ok || request != nil || response != nil {
			t.Errorf("TopicTypes() = (%v, %v, %v), want (nil, nil, false)", request, response, ok)
		}
	})

	t.Run("captures an interface type parameter as the interface", func(t *testing.T) {
		anyTopic := NewTopic("hello:any")
		if err := Register(r, anyTopic, Handler[any, helloResponse](func(_ context.Context, _ any) Result[helloResponse] {
			return Ok(helloResponse{})
		})); err != nil {
			t.Fatalf("Register() error = %v", err)
		}

		request, _, ok := r.TopicTypes(anyTopic)
		if !ok || request == nil || request.Kind() != reflect.Interface {
			t.Errorf("request = %v (ok=%v), want the empty interface type", request, ok)
		}
	})
}
