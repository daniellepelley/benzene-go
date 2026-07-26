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

func TestHTTPHandler_V1MatchedRouteReturnsV1Response(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"httpMethod":"POST","path":"/greet","headers":{"Content-Type":"application/json"},"body":"{\"name\":\"Rest\"}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}

	var resp httpV1Response
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
	if greeting.Greeting != "Hello, Rest!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, Rest!")
	}
	// ALB requires isBase64Encoded on the response; assert the raw JSON carries it even
	// though it is false (omitempty would drop it).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(result, &raw); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if _, ok := raw["isBase64Encoded"]; !ok {
		t.Error(`v1 response JSON is missing "isBase64Encoded"`)
	}
}

func TestHTTPHandler_V1HeadersAreLowerCased(t *testing.T) {
	registry := benzene.NewRegistry()
	echo := func(ctx context.Context, _ struct{}) benzene.Result[string] {
		return benzene.Ok("ok")
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[struct{}, string](echo)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	var seen map[string]string
	capture := benzene.Middleware(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		seen = ic.Headers
		return next(ctx)
	})
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(capture, benzene.RouterMiddleware(registry)),
	}
	handler := HTTPHandler(builder, testRoutes())

	event := `{"httpMethod":"POST","path":"/greet","headers":{"X-Correlation-Id":"abc"},"body":"{}"}`
	if _, err := handler(context.Background(), json.RawMessage(event)); err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if seen["x-correlation-id"] != "abc" {
		t.Errorf(`Headers["x-correlation-id"] = %q, want %q (headers = %v)`, seen["x-correlation-id"], "abc", seen)
	}
}

func TestHTTPHandler_ALBMultiValueHeadersFlattenLastValueWins(t *testing.T) {
	registry := benzene.NewRegistry()
	echo := func(ctx context.Context, _ struct{}) benzene.Result[string] {
		return benzene.Ok("ok")
	}
	if err := benzene.Register(registry, benzene.NewTopic("greet"), benzene.Handler[struct{}, string](echo)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	var seen map[string]string
	capture := benzene.Middleware(func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		seen = ic.Headers
		return next(ctx)
	})
	builder := &benzene.ApplicationBuilder{
		Registry:  registry,
		Container: benzene.NewContainer(),
		Pipeline:  benzene.NewPipeline(capture, benzene.RouterMiddleware(registry)),
	}
	handler := HTTPHandler(builder, testRoutes())

	event := `{"httpMethod":"POST","path":"/greet","multiValueHeaders":{"X-Tag":["first","last"],"X-None":[]},"body":"{}"}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if seen["x-tag"] != "last" {
		t.Errorf(`Headers["x-tag"] = %q, want %q (last value wins)`, seen["x-tag"], "last")
	}
	if _, ok := seen["x-none"]; ok {
		t.Error(`Headers["x-none"] should be absent for a key with no values`)
	}

	var resp httpV1Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, resp.Body)
	}
	// Multi-value ALB mode honors only multiValueHeaders on the response - each outbound
	// header must be echoed there.
	for key, value := range resp.Headers {
		got, ok := resp.MultiValueHeaders[key]
		if !ok || len(got) != 1 || got[0] != value {
			t.Errorf("MultiValueHeaders[%q] = %v, want [%q]", key, got, value)
		}
	}
	if len(resp.Headers) == 0 {
		t.Fatal("expected at least one outbound header (content-type) to assert the multi-value echo against")
	}
}

func TestHTTPHandler_V1UnmatchedRouteIsNotFoundInV1Shape(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"httpMethod":"GET","path":"/no-such-route","headers":{},"body":""}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp httpV1Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(result, &raw); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if _, ok := raw["isBase64Encoded"]; !ok {
		t.Error(`v1 response JSON is missing "isBase64Encoded"`)
	}
}

func TestHTTPHandler_V1Base64EncodedBodyIsDecoded(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	encoded := base64.StdEncoding.EncodeToString([]byte(`{"name":"Alb"}`))
	event := `{"httpMethod":"POST","path":"/greet","headers":{},"body":"` + encoded + `","isBase64Encoded":true}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp httpV1Response
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
	if greeting.Greeting != "Hello, Alb!" {
		t.Errorf("Greeting = %q, want %q", greeting.Greeting, "Hello, Alb!")
	}
}

func TestHTTPHandler_V1MalformedBase64BodyIsBadRequestInV1Shape(t *testing.T) {
	handler := HTTPHandler(newTestBuilder(t), testRoutes())

	event := `{"httpMethod":"POST","path":"/greet","headers":{},"body":"not-valid-base64!!","isBase64Encoded":true}`
	result, err := handler(context.Background(), json.RawMessage(event))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	var resp httpV1Response
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadRequest)
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
