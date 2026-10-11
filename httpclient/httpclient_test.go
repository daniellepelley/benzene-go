package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	return httptest.NewServer(httpbinding.EnvelopeHandler(newTestBuilder(t)))
}

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
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
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
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

// stubResponse is a Client whose every request is answered with code and body.
func stubResponse(code int, body string) *Client {
	return &Client{Endpoint: "http://example.invalid", HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: code, Body: io.NopCloser(bytesReader(body)), Header: make(http.Header)}, nil
		}),
	}}
}

// transport-bindings.md §1: an envelope-over-HTTP client reads the envelope from the body whatever
// the HTTP status, and falls back to the reverse HTTP table only when the body is not an envelope.
func TestSend_NonEnvelopeResponseFallsBackToTheReverseHTTPTable(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want benzene.Status
	}{
		{"a proxy's 502", 502, "", benzene.StatusServiceUnavailable},
		{"a gateway's 401", 401, "<html>denied</html>", benzene.StatusUnauthorized},
		{"the wrong URL", 404, "404 page not found", benzene.StatusNotFound},
		{"a 200 that is not an envelope", 200, "{not valid", benzene.StatusOk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stubResponse(tt.code, tt.body).Send(context.Background(), benzene.NewTopic("greet"), nil, nil)
			if result.Status != tt.want {
				t.Errorf("Status = %q, want %q", result.Status, tt.want)
			}
			if result.Payload != nil {
				t.Errorf("Payload = %s, want none for a body that is not JSON", *result.Payload)
			}
		})
	}

	result := stubResponse(200, `{"some":"json"}`).Send(context.Background(), benzene.NewTopic("greet"), nil, nil)
	if result.Status != benzene.StatusOk || result.Payload == nil || string(*result.Payload) != `{"some":"json"}` {
		t.Errorf("a JSON 200 that is not an envelope = %+v, want ok carrying the body", result)
	}
}

// core-concepts.md §12 error fidelity: a failure that arrives as a bare problem document - the
// application/problem+json body of a route-mapped HTTP binding, or a JSON problem from anything in
// front of one - is read like the problem in a failure envelope, field and code intact.
func TestSend_ProblemDocumentResponseRebuildsErrors(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want benzene.Result[json.RawMessage]
	}{
		{
			name: "errors with field and code",
			code: 422,
			body: `{"type":"https://benzene.app/problems/validation-error","status":422,"benzeneStatus":"validation-error","detail":"Name must not be empty","errors":[{"message":"Name must not be empty","field":"/name","code":"NotEmptyValidator"}]}`,
			want: benzene.Result[json.RawMessage]{Status: benzene.StatusValidationError, Errors: []benzene.Error{{Message: "Name must not be empty", Field: "/name", Code: "NotEmptyValidator"}}},
		},
		{
			name: "detail only is one opaque error",
			code: 409,
			body: `{"type":"about:blank","status":409,"detail":"already exists, try another"}`,
			want: benzene.Result[json.RawMessage]{Status: benzene.StatusConflict, Errors: []benzene.Error{{Message: "already exists, try another"}}},
		},
		{
			name: "JSON with no problem members names the code",
			code: 500,
			body: `{"message":"Internal server error"}`,
			want: benzene.Result[json.RawMessage]{Status: benzene.StatusUnexpectedError, Errors: []benzene.Error{{Message: "outbound request returned HTTP 500 with no response envelope"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stubResponse(tt.code, tt.body).Send(context.Background(), benzene.NewTopic("greet"), nil, nil)
			if result.Status != tt.want.Status {
				t.Errorf("Status = %q, want %q", result.Status, tt.want.Status)
			}
			if !reflect.DeepEqual(result.Errors, tt.want.Errors) {
				t.Errorf("Errors = %+v, want %+v", result.Errors, tt.want.Errors)
			}
		})
	}

	// The real thing: a route-mapped httpbinding answering its own problem+json.
	builder := newTestBuilder(t)
	server := httptest.NewServer(httpbinding.Handler(builder, []httpbinding.Route{{Method: http.MethodPost, Path: "/structured", Topic: benzene.NewTopic("structured")}}))
	defer server.Close()
	result := NewClient(server.URL+"/structured").Send(context.Background(), benzene.NewTopic("structured"), nil, []byte(`{}`))
	want := []benzene.Error{{Message: "name is required", Field: "Name", Code: "required"}}
	if result.Status != benzene.StatusValidationError || !reflect.DeepEqual(result.Errors, want) {
		t.Errorf("Send() to a route = %+v, want validation-error with %+v", result, want)
	}
}

func TestSend_EnvelopeIsReadWhateverTheHTTPStatus(t *testing.T) {
	body := `{"statusCode":"not-found","isSuccessful":false,"headers":{},"body":"{\"benzeneStatus\":\"not-found\",\"detail\":\"no order 7\"}"}`
	result := stubResponse(404, body).Send(context.Background(), benzene.NewTopic("greet"), nil, nil)
	if result.Status != benzene.StatusNotFound || len(result.Errors) != 1 || result.Errors[0].Message != "no order 7" {
		t.Errorf("Send() = %+v, want the envelope's not-found with its detail", result)
	}
}

func TestSend_IsSuccessfulDecidesNotTheStatusText(t *testing.T) {
	declined := `{"statusCode":"payment-declined","isSuccessful":false,"headers":{},"body":"{\"benzeneStatus\":\"payment-declined\",\"detail\":\"card declined\"}"}`
	result := stubResponse(200, declined).Send(context.Background(), benzene.NewTopic("pay"), nil, nil)
	if result.IsSuccessful() || result.Status != "payment-declined" || len(result.Errors) != 1 {
		t.Errorf("Send() = %+v, want an unsuccessful payment-declined carrying its error", result)
	}

	partial := `{"statusCode":"partly-done","isSuccessful":true,"headers":{},"body":"{\"done\":2}"}`
	result = stubResponse(200, partial).Send(context.Background(), benzene.NewTopic("pay"), nil, nil)
	if !result.IsSuccessful() || result.Payload == nil {
		t.Errorf("Send() = %+v, want a successful partly-done carrying its payload", result)
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
