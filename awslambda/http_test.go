package awslambda

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
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
	return benzene.Ok(greetResponse{Greeting: "Hello, " + req.Name + "!"})
}

func newTestBuilder(t *testing.T) *benzene.ApplicationBuilder {
	t.Helper()
	registry := benzene.NewRegistry()
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[greetRequest, greetResponse](greetHandler)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(benzene.RouterMiddleware(registry)),
	}
}

func testRoutes() []httpbinding.Route {
	return []httpbinding.Route{{Method: http.MethodPost, Path: "/greet", Topic: benzene.NewTopic("greet")}}
}

func TestHTTPHandler_MatchedRouteReturnsNativeStatus(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"{\"name\":\"World\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp httpV2Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v; result = %s", err, result)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(resp.Body) error = %v; body = %s", err, resp.Body)
	}
	if greeting.Greeting != "Hello, World!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, World!")
	}
}

func TestHTTPHandler_FailureStatusMapsToNativeHTTPCode(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"{\"name\":\"\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp httpV2Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHTTPHandler_UnmatchedRouteIsNotFound(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"rawPath":"/no-such-route","headers":{},"requestContext":{"http":{"method":"GET","path":"/no-such-route"}},"body":""}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp httpV2Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHTTPHandler_Base64EncodedBodyIsDecoded(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	encoded := base64.StdEncoding.EncodeToString([]byte(`{"name":"Encoded"}`))
	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"` + encoded + `","isBase64Encoded":true}`

	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp httpV2Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
	}
	var greeting greetResponse
	if err := json.Unmarshal([]byte(resp.Body), &greeting); err != nil {
		t.Fatalf("json.Unmarshal(resp.Body) error = %v", err)
	}
	if greeting.Greeting != "Hello, Encoded!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, Encoded!")
	}
}

func TestHTTPHandler_MalformedBase64BodyIsBadRequest(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":"/greet"}},"body":"not-valid-base64!!","isBase64Encoded":true}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp httpV2Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHTTPHandler_MalformedEventIsError(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	if _, err := handler(context.Background(), json.RawMessage("{not valid")); err == nil {
		t.Error("handler() error = nil, want an error for a malformed event")
	}
}

func TestHTTPHandler_FallsBackToRawPathWhenRequestContextPathAbsent(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"rawPath":"/greet","headers":{},"requestContext":{"http":{"method":"POST","path":""}},"body":"{\"name\":\"Raw\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp httpV2Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
	}
}
