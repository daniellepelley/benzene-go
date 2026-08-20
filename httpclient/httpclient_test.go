package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/httpbinding"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greetResponse struct {
	Greeting string `json:"greeting"`
}

func greetHandler(_ context.Context, req greetRequest) benzene.Result[greetResponse] {
	if req.Name == "" {
		return benzene.BadRequest[greetResponse]("name is required")
	}
	return benzene.Ok(greetResponse{Greeting: "Hello " + req.Name})
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	deleteHandler := benzene.Handler[greetRequest, struct{}](func(_ context.Context, _ greetRequest) benzene.Result[struct{}] {
		return benzene.Result[struct{}]{Status: benzene.StatusDeleted}
	})
	if err := benzene.Register(registry, benzene.NewTopic("delete"), deleteHandler); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	structuredHandler := benzene.Handler[greetRequest, greetResponse](func(_ context.Context, _ greetRequest) benzene.Result[greetResponse] {
		return benzene.ValidationErrorWith[greetResponse](
			benzene.Error{Message: "name is required", Field: "Name", Code: "required"})
	})
	if err := benzene.Register(registry, benzene.NewTopic("structured"), structuredHandler); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
	return httptest.NewServer(httpbinding.EnvelopeHandler(builder))
}

func TestSend_SuccessRoundTrip(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()
	client := NewClient(server.URL)

	message, _ := json.Marshal(greetRequest{Name: "World"})
	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{"traceparent": "abc"}, message)

	if result.Status != benzene.StatusOk {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusOk)
	}
	typed, err := Unmarshal[greetResponse](result)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if typed.Payload == nil || typed.Payload.Greeting != "Hello World" {
		t.Errorf("Payload = %+v, want Greeting=Hello World", typed.Payload)
	}
}

func TestSend_FailureCarriesDetail(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()
	client := NewClient(server.URL)

	message, _ := json.Marshal(greetRequest{Name: ""})
	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, message)

	if result.Status != benzene.StatusBadRequest {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusBadRequest)
	}
	if len(result.Errors) == 0 || result.Errors[0].Message != "name is required" {
		t.Errorf("Errors = %v, want [%q]", result.Errors, "name is required")
	}
}

// TestSend_StructuredErrorsSurviveTheRoundTrip is the end-to-end proof that a field and a code
// survive being serialized into a problem document by the server and decoded back by this client.
// The client used to rebuild the Result from ErrorPayload.Messages(), which is the messages alone -
// so a peer that went to the trouble of saying WHICH field failed had that thrown away one hop later.
func TestSend_StructuredErrorsSurviveTheRoundTrip(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()
	client := NewClient(server.URL)

	result := client.Send(context.Background(), benzene.NewTopic("structured"), map[string]string{}, []byte(`{"name":""}`))

	if result.Status != benzene.StatusValidationError {
		t.Fatalf("status = %q, want %q", result.Status, benzene.StatusValidationError)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(result.Errors))
	}
	got := result.Errors[0]
	if got.Message != "name is required" || got.Field != "Name" || got.Code != "required" {
		t.Errorf("Errors[0] = %+v, want the message, field and code the server sent", got)
	}
}

// A peer that sends only `detail` - no errors array - still yields one message-only error rather
// than nothing, and specifically is NOT split on ", " (a rule RFC 9457 withdrew, since messages
// contain commas).
func TestSend_DetailOnlyProblemBecomesOneOpaqueError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"statusCode":"bad-request","headers":{},"body":"{\"benzeneStatus\":\"bad-request\",\"detail\":\"one, two\"}"}`))
	}))
	defer server.Close()

	result := NewClient(server.URL).Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, []byte(`{}`))

	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %+v, want exactly one opaque error", result.Errors)
	}
	if result.Errors[0].Message != "one, two" {
		t.Errorf("Errors[0].Message = %q, want the detail unsplit", result.Errors[0].Message)
	}
}

func TestSend_SuccessWithNoPayload(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()
	client := NewClient(server.URL)

	result := client.Send(context.Background(), benzene.NewTopic("delete"), map[string]string{}, []byte(`{"name":"x"}`))

	if result.Status != benzene.StatusDeleted {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusDeleted)
	}
	if result.Payload != nil {
		t.Errorf("Payload = %+v, want nil", result.Payload)
	}
}

func TestSend_MissingTopicIsNotFound(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()
	client := NewClient(server.URL)

	result := client.Send(context.Background(), benzene.NewTopic("no:such:topic"), map[string]string{}, []byte(""))

	if result.Status != benzene.StatusNotFound {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusNotFound)
	}
}

func TestSend_InvalidEndpointIsServiceUnavailable(t *testing.T) {
	client := NewClient("http://%zz")

	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, []byte(""))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSend_TransportErrorIsServiceUnavailable(t *testing.T) {
	client := &Client{Endpoint: "http://example.invalid", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	}}

	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, []byte(""))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestSend_ResponseBodyReadErrorIsServiceUnavailable(t *testing.T) {
	client := &Client{Endpoint: "http://example.invalid", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: errReadCloser{}, Header: make(http.Header)}, nil
		}),
	}}

	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, []byte(""))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

func TestSend_NonSuccessHTTPStatusIsServiceUnavailable(t *testing.T) {
	client := &Client{Endpoint: "http://example.invalid", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 502, Body: io.NopCloser(bytesReader("")), Header: make(http.Header)}, nil
		}),
	}}

	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, []byte(""))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

func TestSend_MalformedEnvelopeResponseIsServiceUnavailable(t *testing.T) {
	client := &Client{Endpoint: "http://example.invalid", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytesReader("{not valid")), Header: make(http.Header)}, nil
		}),
	}}

	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, []byte(""))

	if result.Status != benzene.StatusServiceUnavailable {
		t.Errorf("Status = %q, want %q", result.Status, benzene.StatusServiceUnavailable)
	}
}

func TestSend_FailureWithMalformedErrorPayloadHasNoErrors(t *testing.T) {
	client := &Client{Endpoint: "http://example.invalid", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := `{"statusCode":"not-found","headers":{},"body":"not json"}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytesReader(body)), Header: make(http.Header)}, nil
		}),
	}}

	result := client.Send(context.Background(), benzene.NewTopic("greet"), map[string]string{}, []byte(""))

	if result.Status != benzene.StatusNotFound {
		t.Fatalf("Status = %q, want %q", result.Status, benzene.StatusNotFound)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", result.Errors)
	}
}

func TestUnmarshal_NoPayload(t *testing.T) {
	typed, err := Unmarshal[greetResponse](benzene.Result[json.RawMessage]{Status: benzene.StatusDeleted})
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if typed.Payload != nil {
		t.Errorf("Payload = %+v, want nil", typed.Payload)
	}
	if typed.Status != benzene.StatusDeleted {
		t.Errorf("Status = %q, want %q", typed.Status, benzene.StatusDeleted)
	}
}

func TestUnmarshal_InvalidPayloadReturnsError(t *testing.T) {
	invalid := json.RawMessage("{not valid")
	_, err := Unmarshal[greetResponse](benzene.Result[json.RawMessage]{Status: benzene.StatusOk, Payload: &invalid})
	if err == nil {
		t.Error("Unmarshal() should return an error for a malformed payload")
	}
}

func bytesReader(s string) io.Reader {
	return &stringReader{s: s}
}

type stringReader struct {
	s string
	i int
}

func (r *stringReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}
